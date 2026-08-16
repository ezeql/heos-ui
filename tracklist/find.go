package tracklist

import (
	"context"
)

// alignedToAudio names sources whose timestamps already refer to this exact
// upload. Calibrating them is at best wasted work and at worst circular:
// aligning cues that were derived from the timed comments against those same
// comments cannot discover anything, it can only invent a shift out of
// sampling noise.
var alignedToAudio = map[string]bool{
	"soundcloud-description": true,
	"soundcloud-comments":    true,
	"shazam":                 true,
	"local":                  true,
}

// Find locates a tracklist for spec and how far to shift it.
//
// It returns (tracklist, offset), with a nil tracklist when no source has
// one. The offset is in seconds and is meant to be subtracted from the
// playback position before looking a cue up.
//
// It never fails: a set with no tracklist, an unreachable database and a
// malformed page all come back as nil, because none of them is a reason for
// the display to stop showing what it showed before.
func Find(ctx context.Context, spec string, opts ...Option) (*Tracklist, float64) {
	o := defaultsOptions()
	for _, opt := range opts {
		opt(&o)
	}

	found := resolve(ctx, spec, &o)
	if found == nil {
		return nil, 0
	}

	// An offset already recorded for this set outranks anything measured now:
	// it is either what the user set by hand or an earlier calibration.
	if o.cache != nil {
		if stored := o.cache.OffsetFor(spec); stored != 0 {
			return found, stored
		}
	}

	if o.calibrate {
		offset := calibrate(ctx, spec, found, &o)
		if offset != 0 && o.cache != nil {
			o.cache.SetOffset(spec, offset)
		}
		return found, offset
	}

	return found, 0
}

// calibrate measures the shift needed to line this tracklist up with the
// audio. Only worth doing for a tracklist written against some other
// recording of the set — a database entry. Everything else was already
// measured against this upload.
func calibrate(ctx context.Context, spec string, tracklist *Tracklist, o *options) float64 {
	if alignedToAudio[tracklist.Source] {
		return 0
	}
	if !isSoundcloud(spec) {
		return 0
	}
	info, err := o.extract(ctx, spec, true)
	if err != nil {
		// Calibration is a bonus on top of a tracklist we already have.
		o.logger.Debug("calibration skipped", "error", err)
		return 0
	}
	duration := tracklist.Duration
	if duration == 0 {
		duration = info.Duration
	}
	offset, confidence := EstimateOffset(tracklist.Cues, commentTimes(info.Comments), duration)
	if confidence == 0 {
		return 0
	}
	return offset
}
