// Package config locates heoscli's files and writes them safely.
package config

import (
	"os"
	"path/filepath"
)

// Path is name inside heoscli's config dir: ~/.config/heoscli on every
// platform, not os.UserConfigDir, because the Python heoscli keeps its files
// there and both tools share them. Without a home directory it is name in the
// working directory.
func Path(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", name)
	}
	return filepath.Join(home, ".config", "heoscli", name)
}

// WriteAtomic replaces path with data. It writes a sibling and renames it into
// place, so a crash mid-write never leaves a half-written file where the old
// one was.
func WriteAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	// os.WriteFile keeps the mode of a .tmp that already existed. The data is
	// in place by now, so a failure here is not a failed write.
	_ = os.Chmod(path, 0o600)
	return nil
}
