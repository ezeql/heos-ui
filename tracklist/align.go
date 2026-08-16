package tracklist

// Lining a tracklist up with the audio it describes.
//
// A tracklist taken from a database was written against some recording of the
// set, and the upload on SoundCloud is rarely that same recording second for
// second — an intro gets trimmed, an outro gets faded, the broadcast version
// is two minutes shorter. Every cue is then off by the same amount, which is
// exactly the kind of error worth correcting because one number fixes all of
// them.
//
// The signal used is the timed comments. People comment right after a track
// changes ("tune!!", "ID?"), so the density of comments over the set has
// bumps that line up with the cue points. Sliding the tracklist against that
// density and keeping the best fit recovers the shift.
//
// This corrects the tracklist against the audio. It says nothing about the
// delay between our clock and what the speaker is actually playing.

const (
	// binSeconds is the resolution of the density histogram. Finer than this
	// is false precision: people take a few seconds to react and to type.
	binSeconds = 5.0

	// maxShift is how far the tracklist is allowed to slide. Three minutes
	// covers a trimmed intro or a missing opener; beyond that it is a
	// different recording, and matching would be a coincidence rather than an
	// alignment.
	maxShift  = 180.0
	shiftStep = 1.0

	// reactionWindow: comments cluster just after a track lands, not on the
	// beat of the change.
	reactionWindow = 20.0

	// minComments: below this many timed comments the histogram is noise and
	// any peak in it is an artefact.
	minComments = 20

	// minConfidenceRatio is how many times the average shift a peak must
	// score before it is believed. Comments that are really tracking the cues
	// beat the average several times over; chatter spread across the set
	// scores about average everywhere, which is what this number rejects.
	minConfidenceRatio = 2.0
)

func histogram(times []float64, duration float64) []float64 {
	bins := int(duration/binSeconds) + 1
	if bins < 1 {
		bins = 1
	}
	counts := make([]float64, bins)
	for _, moment := range times {
		if moment >= 0 && moment <= duration {
			counts[int(moment/binSeconds)]++
		}
	}
	return counts
}

// score measures how much comment activity falls just after each shifted cue.
func score(cues []Cue, counts []float64, duration, shift float64) float64 {
	total := 0.0
	window := int(reactionWindow / binSeconds)
	if window == 0 {
		window = 1
	}
	for _, cue := range cues {
		moment := cue.Start + shift
		if moment < 0 || moment > duration {
			continue
		}
		first := int(moment / binSeconds)
		for i := first; i < first+window && i < len(counts); i++ {
			total += counts[i]
		}
	}
	return total / float64(len(cues))
}

// background is what an uninformative shift scores, for the peak to be
// measured against. The mean, not a high percentile: reactions cluster in a
// few seconds around each change, so most candidate shifts catch nothing and
// score zero. A percentile would land on one of those zeroes and make every
// peak look infinitely good, which is the opposite of a confidence measure.
func background(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	total := 0.0
	for _, s := range scores {
		total += s
	}
	return total / float64(len(scores))
}

// EstimateOffset finds the best shift for these cues, and how much to believe
// it. It returns (seconds, confidence), where a positive shift means the cues
// happen later in the audio than the tracklist claims. Confidence runs 0..1
// and is 0 when the answer is "no idea" — the caller should then not shift at
// all, because an invented correction is worse than none.
func EstimateOffset(cues []Cue, commentTimes []float64, duration float64) (offset, confidence float64) {
	var usable []float64
	for _, t := range commentTimes {
		if t >= 0 {
			usable = append(usable, t)
		}
	}
	if len(cues) == 0 || duration <= 0 || len(usable) < minComments {
		return 0, 0
	}

	counts := histogram(usable, duration)
	any := false
	for _, c := range counts {
		if c > 0 {
			any = true
			break
		}
	}
	if !any {
		return 0, 0
	}

	steps := int(2*maxShift/shiftStep) + 1
	shifts := make([]float64, steps)
	scores := make([]float64, steps)
	for i := range steps {
		shifts[i] = -maxShift + float64(i)*shiftStep
		scores[i] = score(cues, counts, duration, shifts[i])
	}

	best := 0
	for i, s := range scores {
		if s > scores[best] {
			best = i
		}
	}
	peak := scores[best]
	base := background(scores)
	if peak <= 0 || base <= 0 {
		return 0, 0
	}

	ratio := peak / base
	if ratio < minConfidenceRatio {
		// The tracklist fits everywhere equally badly, which means the
		// comments are not telling us anything about it.
		return 0, 0
	}

	// Map the ratio onto 0..1 so callers can show it without knowing the
	// scale.
	confidence = min(1.0, (ratio-minConfidenceRatio)/minConfidenceRatio)
	return shifts[best], confidence
}
