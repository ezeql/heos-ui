// Package tracklist answers "which track of a DJ set is playing".
//
// A set streamed through the proxy reaches the speaker as a single hour-long
// URL stream, so the firmware reports "Url Stream" and nothing else for the
// whole hour. This package finds a tracklist for the set, lines it up with
// the audio, and maps a playback position to the track on right now.
//
// It is a Go port of the heoscli Python tracklist backend. Acoustic
// identification (Shazam-style sampling) is deliberately not ported.
//
// Entry point is Find; the result is a Tracklist whose At maps a position in
// seconds to a CueState.
package tracklist

import "sort"

// unknownMarkers is what a track called "ID" means: the set has it, nobody
// has named it yet.
var unknownMarkers = map[string]bool{
	"id": true, "id?": true, "???": true, "??": true,
	"unknown": true, "unreleased": true, "tba": true,
}

// Cue is one track within a set, at the second it starts.
type Cue struct {
	Start   float64
	Title   string
	Artist  string
	Label   string
	Unknown bool
}

// Describe renders the cue the way a listing would print it.
func (c Cue) Describe() string {
	base := c.Title
	if c.Artist != "" {
		base = c.Artist + " — " + c.Title
	}
	if c.Label != "" {
		return base + " [" + c.Label + "]"
	}
	return base
}

// CueState is which cue is playing, and how far it is from its neighbours.
type CueState struct {
	Index   int
	Total   int
	Current Cue
	// Next is nil on the last cue of the set.
	Next    *Cue
	Elapsed float64
	// Remaining is meaningful only when RemainingKnown: the last cue of a set
	// of unknown length has no end.
	Remaining      float64
	RemainingKnown bool
}

// Tracklist is the cue points for one set, as parsed from one source.
//
// The offset correction deliberately lives elsewhere: this is the parse
// result, cached as-is, while the correction belongs to a playback session.
type Tracklist struct {
	Cues   []Cue
	Source string
	Origin string
	// Duration of the recording in seconds; zero when unknown.
	Duration float64
}

// Len returns the number of cues.
func (t *Tracklist) Len() int { return len(t.Cues) }

// At returns the cue playing at position seconds, or nil.
//
// Nil also covers the intro: a tracklist whose first cue is at 1:30 says
// nothing about the first ninety seconds, and guessing would be worse than
// showing nothing.
func (t *Tracklist) At(position float64) *CueState {
	if len(t.Cues) == 0 {
		return nil
	}
	// The lookup runs twice a second for an hour: bisect, not scan.
	index := sort.Search(len(t.Cues), func(i int) bool {
		return t.Cues[i].Start > position
	}) - 1
	if index < 0 {
		return nil
	}

	current := t.Cues[index]
	state := &CueState{
		Index:   index,
		Total:   len(t.Cues),
		Current: current,
		Elapsed: max(0, position-current.Start),
	}
	switch {
	case index+1 < len(t.Cues):
		next := t.Cues[index+1]
		state.Next = &next
		state.Remaining = max(0, next.Start-position)
		state.RemainingKnown = true
	case t.Duration > 0:
		state.Remaining = max(0, t.Duration-position)
		state.RemainingKnown = true
	}
	return state
}
