package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var kitchenAndPatio = []playerView{
	{ID: 1, Name: "Back Patio", State: "stop"},
	{ID: 2, Name: "Kitchen2", State: "play", Playing: true},
}

func TestResolveTargetByName(t *testing.T) {
	t.Parallel()
	resolved, err := resolveTarget(kitchenAndPatio, nil, "kitchen2")
	if err != nil || resolved.playerID != 2 || resolved.isGroup {
		t.Errorf("resolved = %+v, err = %v", resolved, err)
	}
}

func TestResolveTargetByID(t *testing.T) {
	t.Parallel()
	resolved, err := resolveTarget(kitchenAndPatio, nil, "1")
	if err != nil || resolved.playerID != 1 {
		t.Errorf("resolved = %+v, err = %v", resolved, err)
	}
}

func TestResolveTargetByUnambiguousPartialName(t *testing.T) {
	t.Parallel()
	resolved, err := resolveTarget(kitchenAndPatio, nil, "patio")
	if err != nil || resolved.playerID != 1 {
		t.Errorf("resolved = %+v, err = %v", resolved, err)
	}
}

func TestResolveTargetPrefersTheGroup(t *testing.T) {
	t.Parallel()
	groups := []groupView{{ID: 1, Name: "Everywhere", Leader: 1, Members: []int{2}}}
	resolved, err := resolveTarget(kitchenAndPatio, groups, "")
	if err != nil || !resolved.isGroup || resolved.playerID != 1 {
		t.Errorf("resolved = %+v, err = %v", resolved, err)
	}
	if got := resolved.describe(); got != "Everywhere (Back Patio, Kitchen2)" {
		t.Errorf("describe = %q", got)
	}
}

func TestResolveTargetSeveralUngroupedSpeakersIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := resolveTarget(kitchenAndPatio, nil, ""); err == nil {
		t.Error("expected an error with several ungrouped speakers")
	}
}

func TestResolveTargetSingleSpeakerIsTheDefault(t *testing.T) {
	t.Parallel()
	resolved, err := resolveTarget(kitchenAndPatio[:1], nil, "")
	if err != nil || resolved.playerID != 1 {
		t.Errorf("resolved = %+v, err = %v", resolved, err)
	}
}

func TestResolveTargetUnknownNameListsTheKnownOnes(t *testing.T) {
	t.Parallel()
	_, err := resolveTarget(kitchenAndPatio, nil, "Bathroom")
	if err == nil || !strings.Contains(err.Error(), "Back Patio") {
		t.Errorf("err = %v", err)
	}
}

func TestPickPlayerPrefersThePlayingOne(t *testing.T) {
	t.Parallel()
	// No target resolvable: the page still has to show something.
	shown, ok := pickPlayer(kitchenAndPatio, nil)
	if !ok || shown.ID != 2 {
		t.Errorf("shown = %+v", shown)
	}
}

// -- library ---------------------------------------------------------------

func testLibrary(t *testing.T) *Library {
	t.Helper()
	return NewLibrary(filepath.Join(t.TempDir(), "playlists.json"))
}

func TestLibraryRoundTrip(t *testing.T) {
	t.Parallel()
	library := testLibrary(t)
	if _, err := library.Create("Fiesta"); err != nil {
		t.Fatal(err)
	}
	artist := "Tiga"
	if _, err := library.Add("fiesta", PlaylistItem{
		URL: "https://soundcloud.com/tiga/set", Title: "Set", Artist: &artist,
	}); err != nil {
		t.Fatal(err)
	}

	reopened := NewLibrary(library.path)
	playlist, err := reopened.Get("Fiesta")
	if err != nil {
		t.Fatal(err)
	}
	if len(playlist.Items) != 1 || playlist.Items[0].Title != "Set" {
		t.Errorf("playlist = %+v", playlist)
	}
}

func TestLibraryRejectsDuplicates(t *testing.T) {
	t.Parallel()
	library := testLibrary(t)
	_, _ = library.Create("A")
	item := PlaylistItem{URL: "http://x/1.mp3", Title: "One"}
	if _, err := library.Add("A", item); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Add("A", item); err == nil {
		t.Error("duplicate URL must be rejected")
	}
	if _, err := library.Create("a"); err == nil {
		t.Error("duplicate name must be rejected case-insensitively")
	}
}

func TestLibraryMoveReorders(t *testing.T) {
	t.Parallel()
	library := testLibrary(t)
	_, _ = library.Create("A")
	for _, title := range []string{"One", "Two", "Three"} {
		if _, err := library.Add("A", PlaylistItem{URL: "http://x/" + title, Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	playlist, err := library.Move("A", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if playlist.Items[0].Title != "Three" || playlist.Items[1].Title != "One" {
		t.Errorf("items = %+v", playlist.Items)
	}
}

func TestLibraryCorruptFileStartsEmpty(t *testing.T) {
	t.Parallel()
	library := testLibrary(t)
	writeCorrupt(t, library.path)
	if got := library.All(); len(got) != 0 {
		t.Errorf("playlists = %+v", got)
	}
	if _, err := library.Create("Fresh"); err != nil {
		t.Errorf("library unusable after corrupt start: %v", err)
	}
}

func writeCorrupt(t *testing.T, path string) {
	t.Helper()
	if err := writeFileHelper(path, "{not json"); err != nil {
		t.Fatal(err)
	}
}

func writeFileHelper(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
