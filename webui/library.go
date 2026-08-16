package webui

// User-created playlists, stored locally.
//
// These are heoscli's own lists, unrelated to HEOS playlists (sid=1025),
// which live on the speaker and need an account. A list here is just an
// ordered set of links, so anything playable can go in.
//
// Saved as JSON next to the config, in the same file and schema as the
// Python implementation, so both can share it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PlaylistItem is one entry. URL is what gets played; the rest is for
// display.
type PlaylistItem struct {
	URL      string   `json:"url"`
	Title    string   `json:"title"`
	Artist   *string  `json:"artist"`
	Duration *float64 `json:"duration"`
	Image    *string  `json:"image"`
}

// Playlist is a named, ordered list of items.
type Playlist struct {
	Name  string
	Items []PlaylistItem
}

// TotalDuration sums the known item durations.
func (p *Playlist) TotalDuration() float64 {
	total := 0.0
	for _, item := range p.Items {
		if item.Duration != nil {
			total += *item.Duration
		}
	}
	return total
}

// Image returns the cover for the list: the first artwork available.
func (p *Playlist) Image() *string {
	for _, item := range p.Items {
		if item.Image != nil && *item.Image != "" {
			return item.Image
		}
	}
	return nil
}

// Library holds every saved playlist, backed by a single JSON file.
type Library struct {
	path      string
	playlists map[string]*Playlist
	loaded    bool
}

// DefaultLibraryPath returns the shared heoscli playlists file.
func DefaultLibraryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "playlists.json")
	}
	return filepath.Join(home, ".config", "heoscli", "playlists.json")
}

// NewLibrary opens a library backed by the given file (the shared default
// when empty). A corrupt file must not make every command fail; it starts
// empty and the next save rewrites it.
func NewLibrary(path string) *Library {
	if path == "" {
		path = DefaultLibraryPath()
	}
	return &Library{path: path, playlists: map[string]*Playlist{}}
}

func (l *Library) load() {
	if l.loaded {
		return
	}
	l.loaded = true
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	var file struct {
		Playlists map[string][]PlaylistItem `json:"playlists"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return
	}
	for name, items := range file.Playlists {
		kept := make([]PlaylistItem, 0, len(items))
		for _, item := range items {
			if item.URL == "" {
				continue
			}
			if item.Title == "" {
				item.Title = item.URL
			}
			kept = append(kept, item)
		}
		l.playlists[name] = &Playlist{Name: name, Items: kept}
	}
}

func (l *Library) save() {
	payload := struct {
		Playlists map[string][]PlaylistItem `json:"playlists"`
	}{Playlists: map[string][]PlaylistItem{}}
	for name, playlist := range l.playlists {
		items := playlist.Items
		if items == nil {
			items = []PlaylistItem{}
		}
		payload.Playlists[name] = items
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o750); err != nil {
		return
	}
	// Write to a sibling and replace: a crash mid-write would otherwise
	// leave a half-written file where the playlists used to be.
	temporary := l.path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return
	}
	if err := os.Rename(temporary, l.path); err != nil {
		_ = os.Remove(temporary)
	}
}

// All returns every playlist, sorted by name.
func (l *Library) All() []*Playlist {
	l.load()
	names := make([]string, 0, len(l.playlists))
	for name := range l.playlists {
		names = append(names, name)
	}
	sort.Strings(names)
	all := make([]*Playlist, len(names))
	for i, name := range names {
		all[i] = l.playlists[name]
	}
	return all
}

// Find looks up by name, case-insensitively.
func (l *Library) Find(name string) *Playlist {
	l.load()
	wanted := strings.ToLower(strings.TrimSpace(name))
	for _, playlist := range l.playlists {
		if strings.ToLower(playlist.Name) == wanted {
			return playlist
		}
	}
	return nil
}

// Get is Find with a user-facing error naming the existing lists.
func (l *Library) Get(name string) (*Playlist, error) {
	if found := l.Find(name); found != nil {
		return found, nil
	}
	known := "none yet"
	if names := l.names(); len(names) > 0 {
		known = strings.Join(names, ", ")
	}
	return nil, fmt.Errorf("no playlist named '%s'. Existing: %s", name, known)
}

func (l *Library) names() []string {
	names := make([]string, 0, len(l.playlists))
	for name := range l.playlists {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Create adds an empty playlist.
func (l *Library) Create(name string) (*Playlist, error) {
	l.load()
	clean := strings.TrimSpace(name)
	if clean == "" {
		return nil, errors.New("a playlist needs a name")
	}
	if l.Find(clean) != nil {
		return nil, fmt.Errorf("a playlist named '%s' already exists", clean)
	}
	playlist := &Playlist{Name: clean}
	l.playlists[clean] = playlist
	l.save()
	return playlist, nil
}

// Delete removes a playlist.
func (l *Library) Delete(name string) error {
	playlist, err := l.Get(name)
	if err != nil {
		return err
	}
	delete(l.playlists, playlist.Name)
	l.save()
	return nil
}

// Add appends an entry, rejecting duplicates by URL.
func (l *Library) Add(name string, item PlaylistItem) (*Playlist, error) {
	playlist, err := l.Get(name)
	if err != nil {
		return nil, err
	}
	for _, existing := range playlist.Items {
		if existing.URL == item.URL {
			return nil, fmt.Errorf("'%s' is already in '%s'", item.Title, playlist.Name)
		}
	}
	playlist.Items = append(playlist.Items, item)
	l.save()
	return playlist, nil
}

// Remove drops the entry at the given index.
func (l *Library) Remove(name string, index int) (*Playlist, error) {
	playlist, err := l.Get(name)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(playlist.Items) {
		return nil, fmt.Errorf("that track is not in '%s'", playlist.Name)
	}
	playlist.Items = append(playlist.Items[:index], playlist.Items[index+1:]...)
	l.save()
	return playlist, nil
}

// Move reorders an entry, which is what makes a playlist worth having.
func (l *Library) Move(name string, index, to int) (*Playlist, error) {
	playlist, err := l.Get(name)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(playlist.Items) {
		return nil, fmt.Errorf("there is no track at position %d", index+1)
	}
	item := playlist.Items[index]
	playlist.Items = append(playlist.Items[:index], playlist.Items[index+1:]...)
	if to < 0 {
		to = 0
	}
	if to > len(playlist.Items) {
		to = len(playlist.Items)
	}
	playlist.Items = append(playlist.Items[:to],
		append([]PlaylistItem{item}, playlist.Items[to:]...)...)
	l.save()
	return playlist, nil
}
