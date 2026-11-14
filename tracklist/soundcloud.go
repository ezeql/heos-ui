package tracklist

// Tracklists that SoundCloud itself is holding.
//
// Two of them, and they are very different in cost. The description comes
// back in the same yt-dlp call that resolving the audio already makes, so it
// is free and it is what most DJs actually fill in. The comments need a
// second, slower call, but they are the only source anchored to *this* upload
// rather than to some recording of the same set.

import (
	"context"
	"net/url"
	"sort"
	"strings"
)

// minCommentLength: comments that mark a track change are the ones naming it.
// Anything shorter is a reaction, not information.
const minCommentLength = 4

// bucketSeconds: two people naming the same drop should not become two cues.
// One per bucket.
const bucketSeconds = 30.0

func isSoundcloud(spec string) bool {
	parsed, err := url.Parse(spec)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "soundcloud.com" || strings.HasSuffix(host, ".soundcloud.com")
}

// DescriptionProvider reads the tracklist the uploader typed into the
// description.
type DescriptionProvider struct {
	extract extractor
}

// Name identifies the provider in Tracklist.Source.
func (*DescriptionProvider) Name() string { return "soundcloud-description" }

// Supports reports whether spec is a SoundCloud link.
func (*DescriptionProvider) Supports(spec string) bool { return isSoundcloud(spec) }

// Fetch parses the upload's description.
func (p *DescriptionProvider) Fetch(ctx context.Context, spec string) (*Tracklist, error) {
	info, err := p.extract(ctx, spec, false)
	if err != nil {
		return nil, err
	}
	if info.Description == "" {
		return nil, nil
	}
	tracklist := ParseText(info.Description, p.Name(), spec, info.Duration)
	return &tracklist, nil
}

// CommentsProvider reconstructs cue points from timed comments.
//
// Last in the chain: crowdsourced, noisy, and occasionally wrong on purpose.
// It earns its place on sets where nobody published a tracklist but the
// comments are full of people naming tracks.
type CommentsProvider struct {
	extract extractor
}

// Name identifies the provider in Tracklist.Source.
func (*CommentsProvider) Name() string { return "soundcloud-comments" }

// Supports reports whether spec is a SoundCloud link.
func (*CommentsProvider) Supports(spec string) bool { return isSoundcloud(spec) }

// Fetch turns comments that name a track into a tracklist.
func (p *CommentsProvider) Fetch(ctx context.Context, spec string) (*Tracklist, error) {
	info, err := p.extract(ctx, spec, true)
	if err != nil {
		return nil, err
	}
	duration := info.Duration

	cues := cuesFromComments(info.Comments, duration)
	if len(cues) == 0 {
		return nil, nil
	}
	return &Tracklist{
		Cues:     cues,
		Source:   p.Name(),
		Origin:   spec,
		Duration: duration,
	}, nil
}

// cuesFromComments turns comments that name a track into cue points.
//
// The offset is taken from the comment itself, never parsed out of its text:
// SoundCloud already knows exactly where in the audio it was left.
func cuesFromComments(comments []mediaComment, duration float64) []Cue {
	type candidate struct {
		moment float64
		cue    Cue
	}
	var candidates []candidate
	for _, comment := range comments {
		text := strings.TrimSpace(comment.Text)
		if comment.StartTime == nil || *comment.StartTime < 0 {
			continue
		}
		moment := *comment.StartTime
		if len(text) < minCommentLength || !hasWordCharacter(text) {
			continue
		}
		if duration > 0 && moment > duration {
			continue
		}
		cue, ok := cueFrom(text, moment)
		// Only comments shaped like "Artist - Title" are naming a track; the
		// rest of the thread is applause.
		if !ok || (cue.Artist == "" && !cue.Unknown) {
			continue
		}
		candidates = append(candidates, candidate{moment: moment, cue: cue})
	}

	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].moment < candidates[j].moment
	})
	var kept []Cue
	for _, c := range candidates {
		if len(kept) > 0 && c.moment-kept[len(kept)-1].Start < bucketSeconds {
			// Same moment, two people: prefer the one that said more.
			if len(c.cue.Describe()) > len(kept[len(kept)-1].Describe()) {
				kept[len(kept)-1] = c.cue
			}
			continue
		}
		kept = append(kept, c.cue)
	}
	return kept
}
