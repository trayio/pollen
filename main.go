// pollen, the stupid file watcher
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
	"time"
)

const (
	Unknown fileType = iota
	File
	Dir
)

// last seen mtimes of all the files we know about
var mtimes = make(map[string]time.Time)

type nothing struct{}

type fileType uint8

type ignoredFunc func(string) bool

type paths struct {
	files []string
	dirs  []string
}

type set struct {
	entries map[string]nothing
}

func newSet(keys []string) *set {
	s := &set{entries: make(map[string]nothing)}
	s.addAll(keys)
	return s
}

func (s *set) addAll(keys []string) {
	for _, k := range keys {
		s.add(k)
	}
}

func (s *set) add(key string) {
	s.entries[key] = nothing{}
}

func (s *set) del(key string) {
	delete(s.entries, key)
}

func (s set) exists(key string) bool {
	_, ok := s.entries[key]
	return ok
}

type stringFlags []string

func (s *stringFlags) String() string {
	return fmt.Sprint(*s)
}

func (s *stringFlags) Set(value string) error {
	for _, v := range strings.Split(value, ",") {
		*s = append(*s, v)
	}
	return nil
}

func execTimeout(name string, command string) error {
	fmt.Printf("%sing...\n", name)
	cmd := exec.Command("/bin/sh", "-c", command)

	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b

	err := cmd.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, name, err)
		return err
	}
	done := make(chan error)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			fmt.Fprintln(os.Stderr, name, err)
			return err
		}
	case <-time.After(20 * time.Second):
		fmt.Fprintln(os.Stderr, name, "timed out")
		return errors.New(fmt.Sprintf("%s timed out"))
	}
	fmt.Println(b.String())
	return nil
}

// runs in its own goroutine
func actionLoop(actions <-chan nothing, buildCmd, restartCmd string) {
	for _ = range actions {
		if err := execTimeout("build", buildCmd); err == nil {
			execTimeout("restart", restartCmd)
		}
	}
}

// runs in its own goroutine
func mtimeLoop(input <-chan *paths, actions chan<- nothing, debug bool) {
	current := <-input
	previous := current
	{
		// we are only interested in changes since the start of the program so we
		// let this be the start time for comparisons because it's easier than
		// going in and stat everything right at the start
		initTime := time.Now()
		for _, entry := range current.files {
			mtimes[entry] = initTime
		}
	}
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if debug {
				fmt.Println("scanning for modifications")
			}
			if needsAction(previous, current) {
				select {
				case actions <- nothing{}:
				default:
					// ensure non-blocking send
				}
			}
			previous = current
		case p := <-input:
			if debug {
				fmt.Printf("crawl result: %#v\n", p)
			}
			current = p
		}
	}
}

func walk(dir string, ignoredf ignoredFunc) *paths {
	var files, dirs []string
	files, dirs, _ = doWalk(dir, ignoredf, files, dirs)
	return &paths{files: files, dirs: dirs}
}

func doWalk(dir string, ignoredf ignoredFunc, files []string, dirs []string) ([]string, []string, fileType) {
	var ft fileType

	f, err := os.Open(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open dir %s: %s\n", dir, err)
		return files, dirs, ft
	}

	names, err := f.Readdirnames(-1)
	f.Close()

	if err != nil {
		// On Linux calling Readdirnames with a file returns an error
		if serr, ok := err.(*os.SyscallError); ok && serr.Err == syscall.ENOTDIR {
			ft = File
		} else {
			fmt.Fprintln(os.Stderr, "Readdirnames", err)
			return files, dirs, ft
		}
	}

	// On OS X Readdirnames doesn't return an error if we call it on a file so we
	// assume it's a file if we found nothing. While this is incorrect people
	// don't often have empty directories and worst case we'll just stat the
	// directory as if it were a file later
	if len(names) == 0 {
		ft = File
	}

	if ft != File {
		ft = Dir
	}

	for _, n := range names {
		p := path.Join(dir, n)
		if ignoredf(p) {
			continue
		}

		var ft fileType
		files, dirs, ft = doWalk(p, ignoredf, files, dirs)
		if ft == Dir {
			dirs = append(dirs, p)
		} else if ft == File {
			files = append(files, p)
		}
	}

	return files, dirs, ft
}

func needsAction(previous *paths, current *paths) bool {
	// we only take these shortcuts for dirs because we want to keep the file
	// mtimes updated
	if len(previous.dirs) != len(current.dirs) {
		return true
	}

	{
		uniondirs := newSet(previous.dirs)
		uniondirs.addAll(current.dirs)

		if len(uniondirs.entries) != len(previous.dirs) {
			return true
		}
	}

	// check if files have been modified since last time we saw them
	now := time.Now()
	var changed bool
	for _, entry := range current.files {
		prevMtime, ok := mtimes[entry]
		if !ok {
			// arbitrary time sufficiently in the past - new file so always modified
			prevMtime = now.Add(-30 * time.Minute)
		}
		if info, err := os.Stat(entry); err == nil {
			if info.ModTime().After(now) {
				fmt.Fprintf(os.Stderr, "WARNING: Skipping '%s' as it was modified in the future: file '%s', system '%s'\n",
					entry, formatTime(info.ModTime()), formatTime(now))
				continue
			}

			if info.ModTime().After(prevMtime) {
				fmt.Println("changed:", entry)
				changed = true
			}

			mtimes[entry] = info.ModTime()
		}
	}

	return changed
}

func formatTime(t time.Time) string {
	return t.Format("15:04:05.000")
}

func ignored(v string, ignore []string) bool {
	for _, ignore := range ignore {
		if v == ignore {
			return true
		}
	}
	return false
}

func main() {
	var ignore stringFlags
	var dir, buildCmd, restartCmd string
	var debug bool
	flag.Var(&ignore, "ignore", "comma-separated list of locations to ignore (relative to dir)")
	flag.StringVar(&dir, "dir", ".", "directory to watch")
	flag.StringVar(&buildCmd, "buildCmd", "echo default build command", "build command")
	flag.StringVar(&restartCmd, "restartCmd", "echo default restart command", "restart command")
	flag.BoolVar(&debug, "debug", false, "debug logging")
	flag.Parse()

	for k, v := range ignore {
		ignore[k] = path.Join(dir, v)
	}

	// buffered so we can always accept the next one while running an action
	actionCh := make(chan nothing, 1)
	go actionLoop(actionCh, buildCmd, restartCmd)

	ignoredf := ignoredFunc(func(v string) bool {
		return ignored(v, ignore)
	})

	mtimeCh := make(chan *paths)
	go mtimeLoop(mtimeCh, actionCh, debug)
	mtimeCh <- walk(dir, ignoredf)

	// dir tree crawl loop
	tick := time.NewTicker(6 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			mtimeCh <- walk(dir, ignoredf)
		}
	}
}
