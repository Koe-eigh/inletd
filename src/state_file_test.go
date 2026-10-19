package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestJSONFileStateStoreLoadsLegacyStateAndPreservesPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	store := JSONFileStateStore{Path: path}
	missing, err := store.Load()
	if err != nil || !reflect.DeepEqual(missing, State{}) {
		t.Fatalf("missing state = %+v, %v", missing, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"initialized":true,"seen":["old"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil || !state.Initialized || !reflect.DeepEqual(state.Seen, []string{"old"}) {
		t.Fatalf("legacy state = %+v, %v", state, err)
	}
	state.Cursor = "new"
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load()
	if err != nil || !reflect.DeepEqual(reloaded, state) {
		t.Fatalf("reloaded state = %+v, %v", reloaded, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state file mode = %v, %v", info, err)
	}
}
