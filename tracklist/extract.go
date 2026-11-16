package tracklist

// Which extractor answers.
//
// SoundCloud is the only site this backend really needs, and the Go client
// reaches it without a binary installed. So it goes first, and yt-dlp becomes
// what it is here for: everything else, and a second chance when the private
// API changes shape.

import (
	"context"

	"github.com/ezeql/heos-ui/soundcloud"
)

// scClient is shared so the scraped client_id is paid for once per process.
var scClient = soundcloud.New()

// defaultExtract prefers the Go SoundCloud client and falls back to yt-dlp.
func defaultExtract(ctx context.Context, spec string, withComments bool) (*mediaInfo, error) {
	if !soundcloud.Is(spec) {
		return ytdlpExtract(ctx, spec, withComments)
	}

	info, err := soundcloudExtract(ctx, scClient, spec, withComments)
	if err == nil {
		return info, nil
	}

	// yt-dlp gets a turn only if it is actually installed. When it is not,
	// its "yt-dlp not installed" would replace the real reason SoundCloud
	// said no, which is the one worth reporting.
	fallback, ytdlpErr := ytdlpExtract(ctx, spec, withComments)
	if ytdlpErr != nil {
		return nil, err
	}
	return fallback, nil
}

// soundcloudExtract reads the same fields out of the private API that yt-dlp
// would have handed over.
func soundcloudExtract(ctx context.Context, client *soundcloud.Client, spec string, withComments bool) (*mediaInfo, error) {
	track, err := client.Resolve(ctx, spec)
	if err != nil {
		return nil, err
	}
	info := &mediaInfo{
		Description: track.Description,
		Duration:    track.Duration,
	}
	if !withComments {
		return info, nil
	}

	comments, err := client.Comments(ctx, track.ID)
	if err != nil {
		// The description came back; comments are the expensive half and
		// their absence is an empty tracklist, not a broken one.
		return info, nil
	}
	info.Comments = make([]mediaComment, 0, len(comments))
	for _, comment := range comments {
		at := comment.At
		info.Comments = append(info.Comments, mediaComment{
			StartTime: &at,
			Text:      comment.Text,
		})
	}
	return info, nil
}
