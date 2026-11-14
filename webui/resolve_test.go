package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A direct audio link plays as-is, titled after its last path segment;
// nothing here touches the network.
func TestDirectLinksPlayAsIs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		spec  string
		title string // empty: not directly playable
	}{
		{"https://example.com/Music/Set.MP3", "Set.MP3"},
		{"http://example.com/a/b/track.flac?token=x", "track.flac"},
		{"https://example.com/stream", ""},
		{"https://example.com/stream?file=a.mp3", ""},
		{"https://example.com/track.mp3.html", ""},
		{"ftp://example.com/track.mp3", ""},
	}
	for _, tt := range tests {
		if got := directlyPlayable(tt.spec); got != (tt.title != "") {
			t.Errorf("directlyPlayable(%q) = %v", tt.spec, got)
			continue
		}
		if tt.title == "" {
			continue
		}
		info, err := resolveSpec(t.Context(), tt.spec)
		if err != nil || info.title != tt.title || info.streamURL != tt.spec {
			t.Errorf("resolveSpec(%q) = %+v, %v", tt.spec, info, err)
		}
	}
}

// A link is never an option: a "link" that starts with a dash must reach
// yt-dlp after "--", or it would be parsed as one of its flags.
func TestALinkNeverReachesYtdlpAsAnOption(t *testing.T) {
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	fake := filepath.Join(dir, "yt-dlp")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argv + "'\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEOSCLI_YTDLP", fake)

	_, _ = resolveSpec(t.Context(), "--version")
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Fields(string(raw))
	if n := len(args); n < 2 || args[n-2] != "--" || args[n-1] != "--version" {
		t.Errorf("yt-dlp argv = %q", args)
	}
}
