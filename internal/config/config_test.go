package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The Python heoscli reads the same files, so the location is not ours to move.
func TestPathIsSharedWithThePythonTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := Path("config.toml"), filepath.Join(home, ".config", "heoscli", "config.toml"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

// A .tmp left over from an earlier crash keeps its mode through os.WriteFile;
// the file that ends up in place must still be private.
func TestWriteAtomicOverALeftoverTemporary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "heoscli", "playlists.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteAtomic(path, []byte("fresh")); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "fresh" {
		t.Errorf("content = %q", raw)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, err = %v", info.Mode().Perm(), err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temporary left behind: %v", err)
	}
}
