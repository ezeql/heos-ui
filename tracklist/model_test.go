package tracklist

// Mapping a playback position onto a cue.
//
// The lookup runs twice a second for an hour, so it is a bisect rather than a
// scan; these are the cases where an off-by-one would name the wrong track.

import (
	"fmt"
	"testing"
)

func listing(starts []float64, duration float64, source string) Tracklist {
	cues := make([]Cue, len(starts))
	for n, start := range starts {
		cues[n] = Cue{
			Start:  start,
			Artist: fmt.Sprintf("Artist %d", n),
			Title:  fmt.Sprintf("Track %d", n),
		}
	}
	return Tracklist{Cues: cues, Source: source, Duration: duration}
}

func build(duration float64, starts ...float64) Tracklist {
	return listing(starts, duration, "test")
}

func TestNothingIsPlayingBeforeTheFirstCue(t *testing.T) {
	t.Parallel()
	// A tracklist starting at 1:30 says nothing about the first 90 seconds.
	tl := build(0, 90, 300)
	if state := tl.At(20); state != nil {
		t.Errorf("state = %+v", state)
	}
}

func TestFindsTheCueThatIsPlaying(t *testing.T) {
	t.Parallel()
	tl := build(0, 0, 300, 600)
	state := tl.At(420)
	if state == nil {
		t.Fatal("no state")
	}
	if state.Index != 1 || state.Current.Title != "Track 1" {
		t.Errorf("state = %+v", state)
	}
	if state.Next == nil || state.Next.Title != "Track 2" {
		t.Errorf("next = %+v", state.Next)
	}
}

func TestACueStartsExactlyOnItsTimestamp(t *testing.T) {
	t.Parallel()
	tl := build(0, 0, 300, 600)
	state := tl.At(300)
	if state.Index != 1 || state.Elapsed != 0 {
		t.Errorf("state = %+v", state)
	}
}

func TestReportsTimeIntoTheTrackAndTimeLeft(t *testing.T) {
	t.Parallel()
	tl := build(0, 0, 300, 600)
	state := tl.At(420)
	if state.Elapsed != 120 || !state.RemainingKnown || state.Remaining != 180 {
		t.Errorf("state = %+v", state)
	}
}

func TestTheLastCueHasNoNext(t *testing.T) {
	t.Parallel()
	tl := build(0, 0, 300, 600)
	state := tl.At(700)
	if state.Index != 2 || state.Next != nil {
		t.Errorf("state = %+v", state)
	}
}

func TestTheLastCueUsesTheDurationForTimeLeft(t *testing.T) {
	t.Parallel()
	tl := build(900, 0, 300, 600)
	state := tl.At(700)
	if !state.RemainingKnown || state.Remaining != 200 {
		t.Errorf("state = %+v", state)
	}
}

func TestTheLastCueOfAnUntimedSetHasNoTimeLeft(t *testing.T) {
	t.Parallel()
	tl := build(0, 0, 300, 600)
	if state := tl.At(700); state.RemainingKnown {
		t.Errorf("state = %+v", state)
	}
}

func TestPositionPastTheEndDoesNotGoNegative(t *testing.T) {
	t.Parallel()
	tl := build(900, 0, 300, 600)
	state := tl.At(1200)
	if !state.RemainingKnown || state.Remaining != 0 {
		t.Errorf("state = %+v", state)
	}
}

func TestAnEmptyTracklistNeverMatches(t *testing.T) {
	t.Parallel()
	empty := Tracklist{Source: "test"}
	if empty.At(0) != nil || empty.Len() != 0 {
		t.Error("empty tracklist matched")
	}
}

func TestBisectAgreesWithAScanOverALongSet(t *testing.T) {
	t.Parallel()
	// Two hundred cues is a normal festival set; the fast path must be right.
	starts := make([]float64, 200)
	for n := range starts {
		starts[n] = float64(n * 20)
	}
	tl := listing(starts, 4000, "test")

	for _, position := range []float64{0, 137, 1999, 4000} {
		state := tl.At(position)
		expected := -1
		for n, start := range starts {
			if start <= position {
				expected = n
			}
		}
		if expected < 0 {
			if state != nil {
				t.Errorf("at(%v) = %+v, want nil", position, state)
			}
			continue
		}
		if state == nil || state.Index != expected {
			t.Errorf("at(%v) index = %+v, want %d", position, state, expected)
		}
	}
}
