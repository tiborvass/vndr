package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/LK4D4/vndr/godl"
)

type depEntry struct {
	ImportPath string
	Rev        string
	RepoPath   string
}

func (d depEntry) String() string {
	return fmt.Sprintf("%s %s\n", d.ImportPath, d.Rev)
}

func parseDeps(r io.Reader, useGomod bool) ([]depEntry, error) {
	var deps []depEntry
	s := bufio.NewScanner(r)
	for s.Scan() {
		ln := strings.TrimSpace(s.Text())
		if strings.HasPrefix(ln, "#") || ln == "" {
			continue
		}
		cidx := strings.Index(ln, "#")
		if cidx > 0 {
			ln = ln[:cidx]
		}
		ln = strings.TrimSpace(ln)
		parts := strings.Fields(ln)
		if len(parts) != 2 && len(parts) != 3 {
			return nil, fmt.Errorf("invalid config format: %s", ln)
		}
		d := depEntry{
			ImportPath: parts[0],
			Rev:        parts[1],
		}
		if len(parts) == 3 {
			d.RepoPath = parts[2]
			if useGomod {
				d.RepoPath = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(d.RepoPath, "git://"), "https://"), ".git")
			}
		}
		deps = append(deps, d)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return deps, nil
}

func cloneAll(vd string, ds []depEntry) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(ds))
	limit := make(chan struct{}, 16)

	for _, d := range ds {
		wg.Add(1)
		go func(d depEntry) {
			var err error
			limit <- struct{}{}
			for i := 0; i < 20; i++ {
				if d.RepoPath != "" {
					log.Printf("\tClone %s to %s, revision %s, attempt %d/20", d.RepoPath, d.ImportPath, d.Rev, i+1)
				} else {
					log.Printf("\tClone %s, revision %s, attempt %d/20", d.ImportPath, d.Rev, i+1)
				}
				if err = cloneDep(vd, d); err == nil {
					errCh <- nil
					wg.Done()
					<-limit
					log.Printf("\tFinished clone %s", d.ImportPath)
					return
				}
				log.Printf("\tClone %s, attempt %d/20 finished with error %v", d.ImportPath, i+1, err)
				time.Sleep(1 * time.Second)
			}
			errCh <- err
			wg.Done()
			<-limit
		}(d)
	}
	wg.Wait()
	close(errCh)
	var errs []string
	for err := range errCh {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("Errors on clone:\n%s", strings.Join(errs, "\n"))
}

func cloneDep(vd string, d depEntry) error {
	vcs, err := godl.Download(d.ImportPath, d.RepoPath, vd, d.Rev)
	if err != nil {
		return fmt.Errorf("%s: %v", d.ImportPath, err)
	}
	return cleanVCS(vcs)
}
