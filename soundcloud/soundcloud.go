// Package soundcloud is a small client for the API SoundCloud's own web
// player uses: enough of it to resolve a link, find a stream the speaker can
// fetch on its own, and read the timed comments a tracklist gets built from.
//
// It exists so the common case — a SoundCloud link — needs no yt-dlp binary
// installed. It is not a replacement for yt-dlp and cannot become one: this
// knows one site, yt-dlp knows a thousand. Everything else still goes there.
//
// The API is the private one, which means it is reached the way the web
// player reaches it: with a client_id lifted out of SoundCloud's own
// JavaScript. That id rotates, so it is cached and re-read whenever the API
// starts refusing it.
package soundcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiBase = "https://api-v2.soundcloud.com"

	// userAgent: the private API answers a browser. It is not a disguise —
	// the requests are the same ones the web player makes.
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

	// commentPage is the API's own page size for comments.
	commentPage = 200

	// maxComments caps how deep a popular set is read. Alignment wants
	// density, not completeness, and a set with thousands of comments would
	// otherwise cost a dozen round trips before the first cue appears.
	maxComments = 1000
)

// ErrNoStream is returned when a track offers no stream over the protocol
// asked for: no progressive MP3, or no unencrypted HLS.
var ErrNoStream = errors.New("soundcloud: no stream of that kind for this track")

// ErrNotATrack is returned for a link that resolves to something other than a
// single track: a playlist, a user, a station.
var ErrNotATrack = errors.New("soundcloud: the link is not a single track")

// Client talks to the API. The zero value is not usable; call New.
type Client struct {
	http *http.Client

	// base and home are where the API and the player live, and assets is how
	// the player's bundles are recognized in it. They are fields rather than
	// constants so a test can stand in for all three.
	base   string
	home   string
	assets *regexp.Regexp

	mu       sync.Mutex
	clientID string
}

// New returns a client with its own HTTP client, so a slow SoundCloud never
// blocks anything else.
func New() *Client {
	return &Client{
		http:   &http.Client{Timeout: 20 * time.Second},
		base:   apiBase,
		home:   homePage,
		assets: assetPattern,
	}
}

// Is reports whether a link belongs to SoundCloud, which is the whole extent
// of what this package can resolve.
func Is(link string) bool {
	parsed, err := url.Parse(link)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "soundcloud.com" || strings.HasSuffix(host, ".soundcloud.com")
}

// Track is one upload, as much of it as anything here needs.
type Track struct {
	ID          int64
	Title       string
	Artist      string
	Artwork     string
	Description string
	Duration    float64 // seconds

	transcodings []transcoding
}

// Comment is a comment left at a position in the audio. Only timed comments
// become one: SoundCloud already knows exactly where in the track it was
// left, which is what makes them worth reading at all.
type Comment struct {
	At   float64 // seconds into the track
	Text string
}

type transcoding struct {
	URL     string `json:"url"`
	Preset  string `json:"preset"`
	Snipped bool   `json:"snipped"`
	Format  struct {
		Protocol string `json:"protocol"`
		MimeType string `json:"mime_type"`
	} `json:"format"`
}

// apiTrack is the slice of the API's track object this package reads.
type apiTrack struct {
	Kind         string `json:"kind"`
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Duration     int64  `json:"duration"`      // milliseconds
	FullDuration int64  `json:"full_duration"` // milliseconds, when a preview is served
	ArtworkURL   string `json:"artwork_url"`
	User         struct {
		Username string `json:"username"`
	} `json:"user"`
	PublisherMetadata struct {
		Artist string `json:"artist"`
	} `json:"publisher_metadata"`
	Media struct {
		Transcodings []transcoding `json:"transcodings"`
	} `json:"media"`
}

// Resolve turns a SoundCloud link into the track behind it.
func (c *Client) Resolve(ctx context.Context, link string) (*Track, error) {
	var raw apiTrack
	if err := c.getJSON(ctx, c.base+"/resolve", url.Values{"url": {link}}, &raw); err != nil {
		return nil, err
	}
	if raw.Kind != "track" || raw.ID == 0 {
		return nil, ErrNotATrack
	}
	return raw.track(), nil
}

func (raw apiTrack) track() *Track {
	// A preview shortens Duration while FullDuration keeps the real length;
	// a tracklist lined up against the preview length would be wrong
	// everywhere after the first cue.
	milliseconds := max(raw.FullDuration, raw.Duration)
	artist := raw.PublisherMetadata.Artist
	if artist == "" {
		artist = raw.User.Username
	}
	return &Track{
		ID:           raw.ID,
		Title:        raw.Title,
		Artist:       artist,
		Artwork:      strings.Replace(raw.ArtworkURL, "-large.", "-t500x500.", 1),
		Description:  raw.Description,
		Duration:     float64(milliseconds) / 1000,
		transcodings: raw.Media.Transcodings,
	}
}

// StreamURL returns a URL the speaker can fetch by itself.
//
// Only the progressive MP3 qualifies. SoundCloud offers the same audio over
// HLS as well, and the firmware will not follow a playlist of segments; for
// a track that has nothing else, see HLSURL.
func (c *Client) StreamURL(ctx context.Context, track *Track) (string, error) {
	return c.locate(ctx, track, "progressive")
}

// HLSURL returns the address of the track's HLS playlist. Many tracks offer
// nothing else any more, and the speaker cannot follow one, so this is for
// something on this machine to convert.
func (c *Client) HLSURL(ctx context.Context, track *Track) (string, error) {
	return c.locate(ctx, track, "hls")
}

// locate asks the API where the track's stream over protocol lives. An
// encrypted stream reports a protocol of its own, so it never matches.
func (c *Client) locate(ctx context.Context, track *Track, protocol string) (string, error) {
	chosen, ok := pick(track.transcodings, protocol)
	if !ok {
		return "", ErrNoStream
	}
	var answer struct {
		URL string `json:"url"`
	}
	if err := c.getJSON(ctx, chosen.URL, nil, &answer); err != nil {
		return "", err
	}
	if answer.URL == "" {
		return "", ErrNoStream
	}
	return answer.URL, nil
}

// pick returns the first transcoding over protocol, preferring a full track
// over a snippet. SoundCloud lists the best quality first.
func pick(all []transcoding, protocol string) (transcoding, bool) {
	var fallback transcoding
	var haveFallback bool
	for _, item := range all {
		if item.Format.Protocol != protocol {
			continue
		}
		if !item.Snipped {
			return item, true
		}
		if !haveFallback {
			fallback, haveFallback = item, true
		}
	}
	return fallback, haveFallback
}

// apiComment is one entry of the comments listing. Timestamp is the position
// within the audio in milliseconds, and it is null on a comment left on the
// track as a whole rather than at a moment in it.
type apiComment struct {
	Body      string `json:"body"`
	Timestamp *int64 `json:"timestamp"`
}

// Comments reads the timed comments on a track, newest pages first, up to an
// internal cap. Untimed comments are dropped: they say nothing about where
// anything is.
func (c *Client) Comments(ctx context.Context, trackID int64) ([]Comment, error) {
	next := fmt.Sprintf("%s/tracks/%d/comments", c.base, trackID)
	query := url.Values{
		"limit":    {strconv.Itoa(commentPage)},
		"threaded": {"0"},
	}

	var comments []Comment
	for next != "" && len(comments) < maxComments {
		var page struct {
			Collection []apiComment `json:"collection"`
			NextHref   string       `json:"next_href"`
		}
		if err := c.getJSON(ctx, next, query, &page); err != nil {
			// Whatever was already read is still worth having: a partial
			// page of comments makes a tracklist, half a page makes a
			// worse one, and neither is a reason to fail.
			if len(comments) > 0 {
				return comments, nil
			}
			return nil, err
		}
		for _, item := range page.Collection {
			if item.Timestamp == nil || *item.Timestamp < 0 {
				continue
			}
			comments = append(comments, Comment{
				At:   float64(*item.Timestamp) / 1000,
				Text: item.Body,
			})
		}
		if len(page.Collection) == 0 {
			break
		}
		// next_href carries its own paging parameters; only the credentials
		// are added back on.
		next, query = page.NextHref, nil
	}
	return comments, nil
}

// getJSON performs one API call, decoding into out.
//
// A refused client_id is the one failure worth retrying: the id rotates, and
// the only way to learn that is to be turned away with the old one.
func (c *Client) getJSON(ctx context.Context, endpoint string, query url.Values, out any) error {
	var lastErr error
	for attempt := range 2 {
		id, err := c.ClientID(ctx)
		if err != nil {
			return err
		}
		body, status, err := c.fetch(ctx, endpoint, query, id)
		switch {
		case err != nil:
			return err
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			lastErr = fmt.Errorf("soundcloud: refused the client_id (HTTP %d)", status)
			c.forget(id)
			if attempt == 0 {
				continue
			}
			return lastErr
		case status != http.StatusOK:
			return fmt.Errorf("soundcloud: HTTP %d from %s", status, trimQuery(endpoint))
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("soundcloud: unreadable answer from %s: %w", trimQuery(endpoint), err)
		}
		return nil
	}
	return lastErr
}

// fetch is one HTTP round trip with the credentials attached.
func (c *Client) fetch(ctx context.Context, endpoint string, query url.Values, id string) ([]byte, int, error) {
	target, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, fmt.Errorf("soundcloud: bad endpoint %q: %w", endpoint, err)
	}
	values := target.Query()
	for key, list := range query {
		for _, item := range list {
			values.Set(key, item)
		}
	}
	values.Set("client_id", id)
	target.RawQuery = values.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, fmt.Errorf("soundcloud: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, response.StatusCode, fmt.Errorf("soundcloud: %w", err)
	}
	return body, response.StatusCode, nil
}

// trimQuery keeps the client_id out of error messages.
func trimQuery(endpoint string) string {
	before, _, _ := strings.Cut(endpoint, "?")
	return before
}
