package tracklist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func testCache(t *testing.T) *Cache {
	t.Helper()
	return NewCache(filepath.Join(t.TempDir(), "tracklists.json"))
}

func sample(source string) Tracklist {
	return Tracklist{
		Cues: []Cue{
			{Start: 0, Title: "Opening", Artist: "First"},
			{Start: 300, Title: "Middle", Artist: "Second", Label: "Some Records"},
			{Start: 600, Title: "ID", Unknown: true},
		},
		Source:   source,
		Origin:   "https://soundcloud.com/dj/set",
		Duration: 3600,
	}
}

const spec = "https://soundcloud.com/dj/set"

func TestSurvivesARoundTrip(t *testing.T) {
	t.Parallel()
	cache := testCache(t)
	cache.Put(spec, sample("soundcloud-description"))

	reopened := NewCache(cache.path)
	got := reopened.Get(spec)
	if got == nil {
		t.Fatal("cache miss after round trip")
	}
	if got.Source != "soundcloud-description" || got.Len() != 3 || got.Duration != 3600 {
		t.Errorf("tracklist = %+v", got)
	}
	if got.Cues[1].Label != "Some Records" || !got.Cues[2].Unknown {
		t.Errorf("cues = %+v", got.Cues)
	}
}

func TestAnUnknownSetIsAMiss(t *testing.T) {
	t.Parallel()
	if testCache(t).Get("https://soundcloud.com/nobody/nothing") != nil {
		t.Error("unexpected hit")
	}
}

func TestNoTemporaryFileIsLeftBehind(t *testing.T) {
	t.Parallel()
	cache := testCache(t)
	cache.Put(spec, sample("test"))
	entries, err := os.ReadDir(filepath.Dir(cache.path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "tracklists.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory holds %v", names)
	}
}

func TestACorruptFileStartsEmpty(t *testing.T) {
	t.Parallel()
	cache := testCache(t)
	if err := os.WriteFile(cache.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cache.Get(spec) != nil {
		t.Error("unexpected hit from corrupt file")
	}
	// And it must still be usable for writing.
	cache.Put(spec, sample("test"))
	if cache.Get(spec) == nil {
		t.Error("cache unusable after corrupt start")
	}
}

func TestADifferentVersionDiscardsEverything(t *testing.T) {
	t.Parallel()
	cache := testCache(t)
	cache.Put(spec, sample("test"))

	raw, err := os.ReadFile(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload["version"] = cacheVersion + 1
	edited, _ := json.Marshal(payload)
	if err := os.WriteFile(cache.path, edited, 0o600); err != nil {
		t.Fatal(err)
	}

	if NewCache(cache.path).Get(spec) != nil {
		t.Error("old-version entries must be discarded")
	}
}

func TestAStaleEntryIsAMiss(t *testing.T) {
	t.Parallel()
	cache := testCache(t)
	cache.Put(spec, sample("test"))
	// Move the clock past the TTL.
	cache.now = func() time.Time { return time.Now().Add(cacheTTL + time.Hour) }
	if cache.Get(spec) != nil {
		t.Error("stale entry must be a miss")
	}
}

func TestOneMalformedEntryDoesNotLoseTheOthers(t *testing.T) {
	t.Parallel()
	cache := testCache(t)
	cache.Put(spec, sample("test"))
	cache.Put("https://soundcloud.com/other/set", sample("test"))

	raw, err := os.ReadFile(cache.path)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Version    int                        `json:"version"`
		Tracklists map[string]json.RawMessage `json:"tracklists"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Tracklists[CacheKey(spec)] = json.RawMessage(`{"cues": [{"start": 0}]}`)
	edited, _ := json.Marshal(payload)
	if err := os.WriteFile(cache.path, edited, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened := NewCache(cache.path)
	if reopened.Get(spec) != nil {
		t.Error("the malformed entry must be dropped")
	}
	if reopened.Get("https://soundcloud.com/other/set") == nil {
		t.Error("the good entry must survive")
	}
}

func TestTheSameSetWrittenDifferentlyIsOneEntry(t *testing.T) {
	t.Parallel()
	variants := []string{
		"https://soundcloud.com/dj/set",
		"https://soundcloud.com/dj/set/",
		"https://www.soundcloud.com/dj/set",
		"https://soundcloud.com/dj/set?in=someone/sets/festival",
		"https://SoundCloud.com/dj/set?utm_source=clipboard",
	}
	first := CacheKey(variants[0])
	for _, variant := range variants[1:] {
		if CacheKey(variant) != first {
			t.Errorf("CacheKey(%q) = %q, want %q", variant, CacheKey(variant), first)
		}
	}
}

func TestEditingALocalFileInvalidatesItsParse(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "set.txt")
	if err := os.WriteFile(path, []byte("0:00 A - One"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := CacheKey(path)
	// A later mtime must change the key.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if CacheKey(path) == before {
		t.Error("editing the file must invalidate the cache key")
	}
}

func TestTheOffsetSurvivesARefresh(t *testing.T) {
	t.Parallel()
	cache := testCache(t)
	cache.Put(spec, sample("test"))
	cache.SetOffset(spec, 42.5)
	// A refresh re-parses the cues but keeps what we measured.
	cache.Put(spec, sample("test"))
	if got := cache.OffsetFor(spec); got != 42.5 {
		t.Errorf("offset = %v, want 42.5", got)
	}
}

func TestAnUnknownSetHasNoOffset(t *testing.T) {
	t.Parallel()
	if got := testCache(t).OffsetFor(spec); got != 0 {
		t.Errorf("offset = %v", got)
	}
}

func TestSettingAnOffsetOnAnUnknownSetIsIgnored(t *testing.T) {
	t.Parallel()
	cache := testCache(t)
	cache.SetOffset(spec, 42.5)
	if got := cache.OffsetFor(spec); got != 0 {
		t.Errorf("offset = %v", got)
	}
}

func TestTheFileDoesNotGrowWithoutBound(t *testing.T) {
	t.Parallel()
	cache := testCache(t)
	// Distinct fetch times so pruning has an order to work with.
	moment := time.Now().Add(-time.Duration(maxEntries+50) * time.Minute)
	cache.now = func() time.Time {
		moment = moment.Add(time.Minute)
		return moment
	}
	for n := range maxEntries + 20 {
		cache.Put(
			"https://soundcloud.com/dj/set-"+strconv.Itoa(n),
			Tracklist{Cues: []Cue{{Start: 0, Title: "T"}}, Source: "test"},
		)
	}
	if len(cache.entries) > maxEntries {
		t.Errorf("entries = %d, want <= %d", len(cache.entries), maxEntries)
	}
	// The newest entry survives, the oldest went first.
	if cache.Get("https://soundcloud.com/dj/set-"+strconv.Itoa(maxEntries+19)) == nil {
		t.Error("newest entry pruned")
	}
	if cache.Get("https://soundcloud.com/dj/set-0") != nil {
		t.Error("oldest entry kept")
	}
}
