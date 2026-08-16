// Package position answers how far into the current track playback is.
//
// A URL stream gives the HEOS firmware no duration, so it reports neither
// position nor length. Three sources cover for that, in descending order of
// trust: a position HEOS itself reported (native sources), the speaker's
// UPnP renderer (answers even for URL streams), and a clock started when the
// play command went out, re-anchored whenever a real reading turns up.
package position

import "time"

// Clock estimates the position for streams the speaker cannot time itself,
// discounting whatever time was spent paused.
type Clock struct {
	// duration in seconds; zero when unknown.
	duration    float64
	started     time.Time
	pausedSince time.Time // zero when not paused
	pausedTotal time.Duration
}

// NewClock starts a clock for a track of the given duration (zero when
// unknown), beginning now.
func NewClock(duration float64) *Clock {
	return &Clock{duration: duration, started: time.Now()}
}

// Sync feeds the speaker's state in so pauses do not advance the clock.
// State is polled every couple of seconds, so repeated calls are harmless.
func (c *Clock) Sync(isPlaying bool) {
	switch {
	case isPlaying && !c.pausedSince.IsZero():
		c.pausedTotal += time.Since(c.pausedSince)
		c.pausedSince = time.Time{}
	case !isPlaying && c.pausedSince.IsZero():
		c.pausedSince = time.Now()
	}
}

// Anchor resets the clock to a position the speaker actually reported.
//
// Estimation drifts: the speaker buffers for a few seconds before any sound
// comes out, and every rebuffer adds more. Over a one-hour set that
// accumulates into being a whole track wrong. A real position replaces the
// estimate rather than being averaged with it.
func (c *Clock) Anchor(position float64) {
	now := time.Now()
	c.started = now.Add(-time.Duration(position * float64(time.Second)))
	c.pausedTotal = 0
	if !c.pausedSince.IsZero() {
		c.pausedSince = now
	}
}

// Position returns the seconds elapsed, capped at the track length.
func (c *Clock) Position() float64 {
	// Read the wall clock once: sampling it twice makes a paused position
	// drift instead of holding still.
	now := time.Now()
	paused := c.pausedTotal
	if !c.pausedSince.IsZero() {
		paused += now.Sub(c.pausedSince)
	}
	elapsed := now.Sub(c.started.Add(paused)).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	if c.duration > 0 && elapsed > c.duration {
		return c.duration
	}
	return elapsed
}

// Finished reports whether the clock ran past the track length. Without a
// known duration it never does.
func (c *Clock) Finished() bool {
	return c.duration > 0 && c.Position() >= c.duration
}
