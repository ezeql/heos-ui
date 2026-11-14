package webui

import "testing"

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
