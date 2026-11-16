package webui

// User-created playlists, stored locally.
//
// These are heoscli's own lists, unrelated to HEOS playlists (sid=1025),
// which live on the speaker and need an account. A list here is just an
// ordered set of links, so anything playable can go in.
//
// Saved as JSON next to the config, in the same file and schema as the
// Python heoscli, so both tools can share it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/ezeql/heoscli-go/internal/config"
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

// clone copies the playlist so a caller can read it after the library's
// lock is released while another request edits the original.
func (p *Playlist) clone() *Playlist {
	if p == nil {
		return nil
	}
	return &Playlist{Name: p.Name, Items: slices.Clone(p.Items)}
}

// Library holds every saved playlist, backed by a single JSON file.
//
// Every HTTP request uses it from its own goroutine, so each exported method
// takes mu once and returns copies; the unexported find and get assume it
// is held.
type Library struct {
	path string

	mu        sync.Mutex
	playlists map[string]*Playlist
	loaded    bool
}

// NewLibrary opens a library backed by the given file (the shared default
// when empty). A corrupt file must not make every command fail; it starts
// empty and the next save rewrites it.
func NewLibrary(path string) *Library {
	if path == "" {
		path = config.Path("playlists.json")
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
	_ = config.WriteAtomic(l.path, raw)
}

// All returns every playlist, sorted by name.
func (l *Library) All() []*Playlist {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	names := make([]string, 0, len(l.playlists))
	for name := range l.playlists {
		names = append(names, name)
	}
	sort.Strings(names)
	all := make([]*Playlist, len(names))
	for i, name := range names {
		all[i] = l.playlists[name].clone()
	}
	return all
}

// Find looks up by name, case-insensitively.
func (l *Library) Find(name string) *Playlist {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.find(name).clone()
}

func (l *Library) find(name string) *Playlist {
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
	l.mu.Lock()
	defer l.mu.Unlock()
	playlist, err := l.get(name)
	return playlist.clone(), err
}

func (l *Library) get(name string) (*Playlist, error) {
	if found := l.find(name); found != nil {
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
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	clean := strings.TrimSpace(name)
	if clean == "" {
		return nil, errors.New("a playlist needs a name")
	}
	if l.find(clean) != nil {
		return nil, fmt.Errorf("a playlist named '%s' already exists", clean)
	}
	playlist := &Playlist{Name: clean}
	l.playlists[clean] = playlist
	l.save()
	return playlist.clone(), nil
}

// Delete removes a playlist.
func (l *Library) Delete(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	playlist, err := l.get(name)
	if err != nil {
		return err
	}
	delete(l.playlists, playlist.Name)
	l.save()
	return nil
}

// Add appends an entry, rejecting duplicates by URL.
func (l *Library) Add(name string, item PlaylistItem) (*Playlist, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	playlist, err := l.get(name)
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
	return playlist.clone(), nil
}

// Remove drops the entry at the given index.
func (l *Library) Remove(name string, index int) (*Playlist, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	playlist, err := l.get(name)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(playlist.Items) {
		return nil, fmt.Errorf("that track is not in '%s'", playlist.Name)
	}
	playlist.Items = append(playlist.Items[:index], playlist.Items[index+1:]...)
	l.save()
	return playlist.clone(), nil
}

// Move reorders an entry, which is what makes a playlist worth having.
func (l *Library) Move(name string, index, to int) (*Playlist, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	playlist, err := l.get(name)
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
	return playlist.clone(), nil
}
