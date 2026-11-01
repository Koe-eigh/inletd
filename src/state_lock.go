package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// acquireStateLock owns a stable sidecar file because Save replaces the state file.
// The caller must hold the returned file until all state access has finished.
func acquireStateLock(statePath string) (*os.File, error) {
	dir, err := filepath.Abs(filepath.Dir(statePath))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(dir, filepath.Base(statePath)+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open state lock %q: %w", lockPath, err)
	}
	if err := lockStateFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("state file %q is already owned or cannot be locked: %w", statePath, err)
	}
	return f, nil
}
