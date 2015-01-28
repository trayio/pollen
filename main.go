// pollen, the stupid file watcher
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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

func main() {
	cmd := os.Args[1]
	ignore := os.Args[2:]

	action := func() {
		o, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput()
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(string(o))
	}

	state := make(chan *paths)
	go func() {
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				var files []string
				var dirs []string

				filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
					if err != nil {
						fmt.Println(path, err)
						// don't stop walking
						return nil
					}
					if info.IsDir() {
						dirs = append(dirs, path)
					} else {
						files = append(files, path)
					}
					return nil
				})
				state <- &paths{files: files, dirs: dirs}
			}
		}
	}()

	go func() {
		previous := &paths{}
		for {
			select {
			case p := <-state:
				filtered := &paths{}
				for _, v := range p.dirs {
					if ignored(v, ignore) {
						continue
					}
					filtered.dirs = append(filtered.dirs, v)
				}
				for _, v := range p.files {
					if ignored(v, ignore) {
						continue
					}
					filtered.files = append(filtered.files, v)
				}

				//fmt.Printf("%#v\n", filtered)
				if needsAction(previous, filtered) {
					fmt.Println("reloading...")
					go action()
				}

				previous = filtered
			}
		}
	}()

	<-(chan bool)(nil)
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