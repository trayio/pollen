// pollen, the stupid file watcher
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"
	"time"
)

type fileType uint8

const (
	Unknown fileType = iota
	File
	Dir
)

type ignoredFunc func(string) bool

type paths struct {
	files []string
	dirs  []string
}

type set struct {
	entries map[string]struct{}
}

func newSet(keys []string) *set {
	s := &set{entries: make(map[string]struct{})}
	s.addAll(keys)
	return s
}

func (s *set) addAll(keys []string) {
	for _, k := range keys {
		s.add(k)
	}
}

func (s *set) add(key string) {
	s.entries[key] = struct{}{}
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

func main() {
	var ignore stringFlags
	var dir, buildCmd, restartCmd string
	var debug bool
	flag.Var(&ignore, "ignore", "comma-separated list of locations to ignore")
	flag.StringVar(&dir, "dir", ".", "directory to watch")
	flag.StringVar(&buildCmd, "buildCmd", "echo default build command", "build command")
	flag.StringVar(&restartCmd, "restartCmd", "echo default restart command", "restart command")
	flag.BoolVar(&debug, "debug", false, "debug logging")
	flag.Parse()

	action := func() {
		{
			fmt.Println("building...")
			o, err := exec.Command("/bin/sh", "-c", buildCmd).CombinedOutput()
			if err != nil {
				fmt.Println(err)
				return
			}
			fmt.Println(string(o))
		}
		{
			fmt.Println("restarting...")
			o, err := exec.Command("/bin/sh", "-c", restartCmd).CombinedOutput()
			if err != nil {
				fmt.Println(err)
				return
			}
			fmt.Println(string(o))
		}
	}

	state := make(chan *paths)
	ignoredf := ignoredFunc(func(v string) bool {
		return ignored(v, ignore)
	})

	{
		current := walk(dir, ignoredf)
		previous := current
		go func() {
			tick := time.NewTicker(3 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-tick.C:
					if debug {
						fmt.Println("scanning for modifications")
					}
					if needsAction(previous, current) {
						go action()
					}
					previous = current
				case p := <-state:
					if debug {
						fmt.Printf("crawl result: %#v\n", p)
					}
					current = p
				}
			}
		}()
	}

	tick := time.NewTicker(7 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			state <- walk(dir, ignoredf)
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
		return files, dirs, ft
	}

	names, err := f.Readdirnames(-1)
	f.Close()

	// Linux
	if err != nil {
		if serr, ok := err.(*os.SyscallError); ok {
			if serr.Err == syscall.ENOTDIR {
				ft = File
			}
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
	if len(previous.dirs) != len(current.dirs) {
		return true
	}

	if len(previous.files) != len(current.files) {
		return true
	}

	uniondirs := newSet(previous.dirs)
	uniondirs.addAll(current.dirs)

	if len(uniondirs.entries) != len(previous.dirs) {
		return true
	}

	unionfiles := newSet(previous.files)
	unionfiles.addAll(current.files)

	if len(unionfiles.entries) != len(previous.files) {
		return true
	}

	for _, entry := range current.files {
		if info, err := os.Stat(entry); err == nil {
			// check if file has been modified in the last couple of seconds
			since := time.Now().Add(-3 * time.Second)
			if since.Before(info.ModTime()) {
				fmt.Println("changed:", entry)
				return true
			}
		}
	}

	return false
}

func ignored(v string, ignore []string) bool {
	for _, ignore := range ignore {
		if strings.HasPrefix(v, ignore) {
			return true
		}
	}
	return false
}
