package tracklist

// A tracklist the user supplied by hand.
//
// The last resort that always works. Web databases go dark and plenty of sets
// have no published tracklist at all. Copying a list into a text file and
// pointing at it takes a few seconds and never breaks.
//
// The file is read with the same parser as everything else, so whatever was
// copied from a forum post or a chat message usually works unchanged.

import (
	"context"
	"os"
	"strings"
)

// maxLocalFileBytes: reading an arbitrarily large file into memory to look
// for timestamps is not worth it — a tracklist for a six-hour set is a few
// kilobytes.
const maxLocalFileBytes = 1 << 20

// LocalFileProvider reads cue points from a text file on disk.
type LocalFileProvider struct{}

// Name identifies the provider in Tracklist.Source.
func (*LocalFileProvider) Name() string { return "local" }

// Supports reports whether spec is an existing local file.
func (*LocalFileProvider) Supports(spec string) bool {
	if strings.Contains(spec, "://") {
		return false
	}
	info, err := os.Stat(expandUser(spec))
	return err == nil && info.Mode().IsRegular()
}

// Fetch parses the file. A file too large or unreadable yields nil.
func (p *LocalFileProvider) Fetch(_ context.Context, spec string, hint *Hint) (*Tracklist, error) {
	path := expandUser(spec)
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxLocalFileBytes {
		return nil, nil
	}
	// Tracklists are full of accented artist names and typographic dashes,
	// and are pasted from everywhere; Go strings pass bad bytes through
	// rather than losing the file.
	raw, err := os.ReadFile(path) //nolint:gosec // reading the user-pointed file is this provider's purpose
	if err != nil {
		return nil, nil
	}
	duration := 0.0
	if hint != nil {
		duration = hint.Duration
	}
	tracklist := ParseText(string(raw), p.Name(), path, duration)
	return &tracklist, nil
}
