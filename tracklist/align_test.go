package tracklist

// Lining a tracklist up with the audio using timed comments.
//
// The property that matters most here is the refusal: when the comments say
// nothing useful, the answer has to be "no shift" rather than a
// plausible-looking number, because a wrong shift is worse than none.

import (
	"fmt"
	"math"
	"testing"
)

const alignDuration = 3600.0

var cueStarts = []float64{0, 300, 620, 900, 1250, 1600, 1950, 2300, 2700, 3100}

func alignCues() []Cue {
	cues := make([]Cue, len(cueStarts))
	for n, start := range cueStarts {
		cues[n] = Cue{
			Start:  start,
			Artist: fmt.Sprintf("Artist %d", n),
			Title:  fmt.Sprintf("Track %d", n),
		}
	}
	return cues
}

// commentsReactingTo simulates comments landing just after each track change,
// as people actually type.
func commentsReactingTo(starts []float64, shift float64) []float64 {
	var times []float64
	for _, start := range starts {
		for n := range 6 {
			moment := start + shift + 3 + float64(n*2)
			if moment >= 0 && moment <= alignDuration {
				times = append(times, moment)
			}
		}
	}
	return times
}

func TestRecoversATracklistThatRunsEarly(t *testing.T) {
	t.Parallel()
	// The upload has a trimmed intro, so every cue lands late in the audio.
	offset, confidence := EstimateOffset(alignCues(), commentsReactingTo(cueStarts, 45), alignDuration)
	if confidence <= 0 || math.Abs(offset-45) > 5 {
		t.Errorf("offset = %v, confidence = %v", offset, confidence)
	}
}

func TestRecoversATracklistThatRunsLate(t *testing.T) {
	t.Parallel()
	offset, confidence := EstimateOffset(alignCues(), commentsReactingTo(cueStarts, -30), alignDuration)
	if confidence <= 0 || math.Abs(offset+30) > 5 {
		t.Errorf("offset = %v, confidence = %v", offset, confidence)
	}
}

func TestAnAlignedTracklistIsLeftAlone(t *testing.T) {
	t.Parallel()
	offset, confidence := EstimateOffset(alignCues(), commentsReactingTo(cueStarts, 0), alignDuration)
	if math.Abs(offset) > 5 || confidence <= 0 {
		t.Errorf("offset = %v, confidence = %v", offset, confidence)
	}
}

func TestEvenlySpreadCommentsSayNothing(t *testing.T) {
	t.Parallel()
	// Chatter with no bumps in it must not produce a shift.
	var spread []float64
	for n := range 500 {
		if moment := float64(n * 7); moment <= alignDuration {
			spread = append(spread, moment)
		}
	}
	offset, confidence := EstimateOffset(alignCues(), spread, alignDuration)
	if offset != 0 || confidence != 0 {
		t.Errorf("offset = %v, confidence = %v", offset, confidence)
	}
}

func TestTooFewCommentsToTell(t *testing.T) {
	t.Parallel()
	few := make([]float64, minComments-1)
	for n := range few {
		few[n] = float64(n * 60)
	}
	if offset, confidence := EstimateOffset(alignCues(), few, alignDuration); offset != 0 || confidence != 0 {
		t.Errorf("offset = %v, confidence = %v", offset, confidence)
	}
}

func TestNoCommentsAtAll(t *testing.T) {
	t.Parallel()
	if offset, confidence := EstimateOffset(alignCues(), nil, alignDuration); offset != 0 || confidence != 0 {
		t.Errorf("offset = %v, confidence = %v", offset, confidence)
	}
}

func TestNoCuesToAlign(t *testing.T) {
	t.Parallel()
	if offset, confidence := EstimateOffset(nil, commentsReactingTo(cueStarts, 0), alignDuration); offset != 0 || confidence != 0 {
		t.Errorf("offset = %v, confidence = %v", offset, confidence)
	}
}

func TestUnknownDurationCannotBeAligned(t *testing.T) {
	t.Parallel()
	if offset, confidence := EstimateOffset(alignCues(), commentsReactingTo(cueStarts, 0), 0); offset != 0 || confidence != 0 {
		t.Errorf("offset = %v, confidence = %v", offset, confidence)
	}
}

func TestTheShiftStaysWithinTheSearchedRange(t *testing.T) {
	t.Parallel()
	offset, _ := EstimateOffset(alignCues(), commentsReactingTo(cueStarts, 600), alignDuration)
	if offset < -180 || offset > 180 {
		t.Errorf("offset = %v", offset)
	}
}
