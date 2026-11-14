package tracklist

// Tracklists kept on disk between runs.
//
// Resolving a tracklist costs a network round trip and, for the comment
// provider, several seconds. A set's tracklist never changes, so it is worth
// keeping.
//
// One JSON file next to the config, written atomically, and a corrupt file
// starts empty rather than breaking every command. The schema is identical to
// the Python heoscli cache, so both tools can share the file.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// cacheVersion is bumped whenever the parser changes in a way that would
	// produce different cues. Old parses are then discarded instead of
	// outliving the code that made them.
	cacheVersion = 1

	// cacheTTL is long, because a tracklist is a fact about a recording, not
	// a live value. The expiry exists to eventually retry sets that had no
	// tracklist when first asked.
	cacheTTL = 30 * 24 * time.Hour

	// maxEntries is enough to hold a heavy listener's history without the
	// file getting silly.
	maxEntries = 200
)

// DefaultCachePath returns the shared heoscli cache location. Python heoscli
// hardcodes ~/.config/heoscli on every platform (not the macOS Application
// Support convention), and sharing the file matters more than the platform
// convention does.
func DefaultCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "tracklists.json")
	}
	return filepath.Join(home, ".config", "heoscli", "tracklists.json")
}

// CacheKey is the identity of a set, stable across the ways the same link
// gets written. SoundCloud hands out links with "?in=<playlist>" when a track
// is opened from inside a set, and campaign parameters get appended by
// everything that touches a URL. All of those are the same audio.
func CacheKey(spec string) string {
	if parsed, err := url.Parse(spec); err == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") {
		host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
		return host + strings.TrimRight(parsed.Path, "/")
	}
	// A local file: the path plus its mtime, so editing it invalidates the
	// parse.
	path := expandUser(spec)
	if info, err := os.Stat(path); err == nil {
		if resolved, err := filepath.Abs(path); err == nil {
			return fmt.Sprintf("file:%s:%d", resolved, info.ModTime().Unix())
		}
	}
	return "file:" + path
}

func expandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path[1:], "/"))
		}
	}
	return path
}

// cacheEntry is a parsed tracklist plus what we learned while playing it.
type cacheEntry struct {
	tracklist Tracklist
	fetchedAt float64
	// offset is the seconds to subtract from our position before looking up a
	// cue. It survives a refresh: it describes the speaker and the set, not
	// the parse.
	offset     float64
	confidence float64
}

// Cache holds every cached tracklist, backed by a single JSON file.
type Cache struct {
	path    string
	entries map[string]*cacheEntry
	loaded  bool
	now     func() time.Time
}

// NewCache opens a cache backed by the given file. The file is read lazily on
// first use; a missing or corrupt file starts empty.
func NewCache(path string) *Cache {
	if path == "" {
		path = DefaultCachePath()
	}
	return &Cache{path: path, entries: map[string]*cacheEntry{}, now: time.Now}
}

// JSON wire format, kept byte-compatible with the Python heoscli:
// nulls where Python writes None.

type cacheFile struct {
	Version    int                       `json:"version"`
	Tracklists map[string]cacheEntryJSON `json:"tracklists"`
}

type cacheEntryJSON struct {
	Source     string    `json:"source"`
	Origin     *string   `json:"origin"`
	Duration   *float64  `json:"duration"`
	FetchedAt  float64   `json:"fetched_at"`
	Offset     float64   `json:"offset"`
	Confidence float64   `json:"confidence"`
	Cues       []cueJSON `json:"cues"`
}

type cueJSON struct {
	Start   float64 `json:"start"`
	Title   *string `json:"title"`
	Artist  *string `json:"artist"`
	Label   *string `json:"label"`
	Unknown bool    `json:"unknown"`
}

func optional[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func value[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func (c *Cache) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var file cacheFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return
	}
	if file.Version != cacheVersion {
		// Not an error: the parser moved on, so the old cues are worthless.
		return
	}
	for key, entry := range file.Tracklists {
		// One malformed entry must not cost the user the other 199; the typed
		// decode above already tolerates missing fields as zero values, but a
		// cue without a title is not a cue.
		cues := make([]Cue, 0, len(entry.Cues))
		valid := true
		for _, cue := range entry.Cues {
			if cue.Title == nil {
				valid = false
				break
			}
			cues = append(cues, Cue{
				Start:   cue.Start,
				Title:   *cue.Title,
				Artist:  value(cue.Artist),
				Label:   value(cue.Label),
				Unknown: cue.Unknown,
			})
		}
		if !valid {
			continue
		}
		source := entry.Source
		if source == "" {
			source = "cache"
		}
		c.entries[key] = &cacheEntry{
			tracklist: Tracklist{
				Cues:     cues,
				Source:   source,
				Origin:   value(entry.Origin),
				Duration: value(entry.Duration),
			},
			fetchedAt:  entry.FetchedAt,
			offset:     entry.Offset,
			confidence: entry.Confidence,
		}
	}
}

func (c *Cache) save() {
	file := cacheFile{Version: cacheVersion, Tracklists: map[string]cacheEntryJSON{}}
	for key, entry := range c.entries {
		cues := make([]cueJSON, 0, len(entry.tracklist.Cues))
		for _, cue := range entry.tracklist.Cues {
			title := cue.Title
			cues = append(cues, cueJSON{
				Start:   cue.Start,
				Title:   &title,
				Artist:  optional(cue.Artist),
				Label:   optional(cue.Label),
				Unknown: cue.Unknown,
			})
		}
		file.Tracklists[key] = cacheEntryJSON{
			Source:     entry.tracklist.Source,
			Origin:     optional(entry.tracklist.Origin),
			Duration:   optional(entry.tracklist.Duration),
			FetchedAt:  entry.fetchedAt,
			Offset:     entry.offset,
			Confidence: entry.confidence,
			Cues:       cues,
		}
	}
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o750); err != nil {
		return
	}
	temporary := c.path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return
	}
	if err := os.Rename(temporary, c.path); err != nil {
		_ = os.Remove(temporary)
		return
	}
	_ = os.Chmod(c.path, 0o600)
}

// Get returns the cached tracklist for a set, or nil on a miss or a stale
// entry.
func (c *Cache) Get(spec string) *Tracklist {
	c.load()
	entry := c.fresh(CacheKey(spec))
	if entry == nil {
		return nil
	}
	tracklist := entry.tracklist
	return &tracklist
}

// OffsetFor returns the offset recorded for a set, or zero.
func (c *Cache) OffsetFor(spec string) float64 {
	c.load()
	if entry := c.entries[CacheKey(spec)]; entry != nil {
		return entry.offset
	}
	return 0
}

func (c *Cache) fresh(key string) *cacheEntry {
	entry := c.entries[key]
	if entry == nil {
		return nil
	}
	if c.now().Sub(time.Unix(int64(entry.fetchedAt), 0)) > cacheTTL {
		return nil
	}
	return entry
}

// Put stores a tracklist. A refresh re-parses the cues but keeps what we
// measured about the speaker: the buffer latency did not change because the
// text did.
func (c *Cache) Put(spec string, tracklist Tracklist) {
	c.load()
	key := CacheKey(spec)
	previous := c.entries[key]
	entry := &cacheEntry{
		tracklist: tracklist,
		fetchedAt: float64(c.now().Unix()),
	}
	if previous != nil {
		entry.offset = previous.offset
		entry.confidence = previous.confidence
	}
	c.entries[key] = entry
	c.prune()
	c.save()
}

// SetOffset records the measured offset for a set already in the cache.
func (c *Cache) SetOffset(spec string, offset float64) {
	c.load()
	entry := c.entries[CacheKey(spec)]
	if entry == nil {
		return
	}
	entry.offset = offset
	c.save()
}

func (c *Cache) prune() {
	if len(c.entries) <= maxEntries {
		return
	}
	keys := make([]string, 0, len(c.entries))
	for key := range c.entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return c.entries[keys[i]].fetchedAt < c.entries[keys[j]].fetchedAt
	})
	for _, key := range keys[:len(c.entries)-maxEntries] {
		delete(c.entries, key)
	}
}
