package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State is small, disposable bookkeeping used for backoff and status.
type State struct {
	FailCount     int    `json:"fail_count"`
	LastAttempt   int64  `json:"last_attempt,omitempty"`
	LastPortalURL string `json:"last_portal_url,omitempty"`
	LastSuccess   int64  `json:"last_success,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

type stateStore struct {
	dir string
	mu  sync.Mutex
}

func newStateStore(dir string) (*stateStore, error) {
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	return &stateStore{dir: dir}, nil
}

func (s *stateStore) path() string {
	return filepath.Join(s.dir, "state.json")
}

func (s *stateStore) load() State {
	var st State
	data, err := os.ReadFile(s.path())
	if err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func (s *stateStore) save(st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := marshalIndent(st)
	if err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

func (s *stateStore) recordFailure(msg string) {
	st := s.load()
	st.FailCount++
	st.LastAttempt = time.Now().Unix()
	st.LastError = msg
	_ = s.save(st)
}

func (s *stateStore) recordSuccess(portalURL string) {
	st := s.load()
	st.FailCount = 0
	st.LastAttempt = time.Now().Unix()
	st.LastSuccess = st.LastAttempt
	st.LastError = ""
	if portalURL != "" {
		st.LastPortalURL = portalURL
	}
	_ = s.save(st)
}

func (s *stateStore) inBackoff() bool {
	st := s.load()
	if st.FailCount < failThreshold {
		return false
	}
	return time.Since(time.Unix(st.LastAttempt, 0)) < cooldown
}

// clearFailures drops the long cooldown after a network change. Failures from
// the previous network (wrong portal, stale client MAC, no DHCP) should not
// delay the first login attempt on the new one.
func (s *stateStore) clearFailures() {
	st := s.load()
	if st.FailCount == 0 {
		return
	}
	st.FailCount = 0
	_ = s.save(st)
}

// lock is a portable best-effort inter-process lock. A stale lock older than
// lockStaleAfter is removed, so a crashed run cannot block the scheduler.
type lock struct {
	path string
	held bool
}

const (
	failThreshold  = 5
	cooldown       = 5 * time.Minute
	lockStaleAfter = 10 * time.Minute
)

func acquireLock(dir string) (*lock, bool, error) {
	path := filepath.Join(dir, "lock")
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d %d\n", os.Getpid(), time.Now().Unix())
			_ = f.Close()
			return &lock{path: path, held: true}, true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		if time.Since(info.ModTime()) > lockStaleAfter {
			_ = os.Remove(path)
			continue
		}
		return nil, false, nil
	}
	return nil, false, nil
}

func (l *lock) release() {
	if l == nil || !l.held {
		return
	}
	_ = os.Remove(l.path)
	l.held = false
}
