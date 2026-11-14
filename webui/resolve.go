package webui

// Turning a pasted link into something the speaker can fetch.
//
// The Python implementation resolves through yt-dlp as a library and streams
// through a local proxy when the format does not fit the speaker. This port
// covers the base routes only: a direct http(s) audio URL plays as-is, a
// SoundCloud link is resolved to its progressive MP3 by the Go client, and
// anything else falls to yt-dlp when it is installed. The proxy —
// transcoding, live streams, multi-track sets fed one by one — is
// deliberately not ported yet.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"

	"github.com/ezeql/heoscli-go/soundcloud"
)

// scClient is shared so the scraped client_id is paid for once per process.
var scClient = soundcloud.New()

// trackInfo is what playback needs to know about a resolved link.
type trackInfo struct {
	title     string
	artist    string
	image     string
	duration  float64 // seconds; 0 when unknown
	streamURL string  // what the speaker fetches
}

// directlyPlayable reports whether the link itself is something the firmware
// can fetch without help: plain http(s) audio.
func directlyPlayable(spec string) bool {
	parsed, err := url.Parse(spec)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	ext := path.Ext(strings.ToLower(parsed.Path))
	return slices.Contains([]string{".mp3", ".aac", ".m4a", ".flac", ".wav", ".ogg"}, ext)
}

// resolveSpec turns a link into a playable stream plus display metadata.
func resolveSpec(ctx context.Context, spec string) (*trackInfo, error) {
	if directlyPlayable(spec) {
		name := spec
		if parsed, err := url.Parse(spec); err == nil && parsed.Path != "" {
			name = path.Base(parsed.Path)
		}
		return &trackInfo{title: name, streamURL: spec}, nil
	}
	if soundcloud.Is(spec) {
		info, err := soundcloudResolve(ctx, spec)
		if err == nil {
			return info, nil
		}
		// A playlist is a playlist however it is resolved; yt-dlp would only
		// arrive at the same answer more slowly.
		if errors.Is(err, soundcloud.ErrNotATrack) {
			return nil, errors.New("that is a set/playlist; playing those needs the proxy, which this backend does not carry yet — play a single track")
		}
		// Otherwise yt-dlp gets a turn, but only if it is installed: its
		// "not installed" would replace the real reason SoundCloud said no.
		fallback, ytdlpErr := ytdlpResolve(ctx, spec)
		if ytdlpErr != nil {
			if errors.Is(err, soundcloud.ErrNoStream) {
				return nil, errors.New("this track only offers a stream the speaker cannot fetch directly (hls)")
			}
			return nil, fmt.Errorf("could not resolve the link: %w", err)
		}
		return fallback, nil
	}
	return ytdlpResolve(ctx, spec)
}

// soundcloudResolve resolves a SoundCloud link without any binary installed.
func soundcloudResolve(ctx context.Context, spec string) (*trackInfo, error) {
	track, err := scClient.Resolve(ctx, spec)
	if err != nil {
		return nil, err
	}
	stream, err := scClient.StreamURL(ctx, track)
	if err != nil {
		return nil, err
	}
	return &trackInfo{
		title:     track.Title,
		artist:    track.Artist,
		image:     track.Artwork,
		duration:  track.Duration,
		streamURL: stream,
	}, nil
}

// ytdlpResolve asks yt-dlp for the direct audio URL and metadata. Prefers a
// progressive http format the firmware can fetch on its own.
func ytdlpResolve(ctx context.Context, spec string) (*trackInfo, error) {
	binary := os.Getenv("HEOSCLI_YTDLP")
	if binary == "" {
		found, err := exec.LookPath("yt-dlp")
		if err != nil {
			return nil, errors.New("yt-dlp is not installed, so this link cannot be resolved")
		}
		binary = found
	}
	//nolint:gosec // fixed binary; the spec is the user's own link
	cmd := exec.CommandContext(ctx, binary,
		"--no-download", "--no-warnings", "--no-playlist",
		"-f", "http_mp3*/http*/bestaudio[protocol^=http]/best[protocol^=http]",
		"-J", spec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		reason := strings.TrimSpace(strings.SplitN(stderr.String(), "\n", 2)[0])
		if reason == "" {
			reason = err.Error()
		}
		return nil, fmt.Errorf("could not resolve the link: %s", reason)
	}

	var info struct {
		Title     string  `json:"title"`
		Uploader  string  `json:"uploader"`
		Artist    string  `json:"artist"`
		Thumbnail string  `json:"thumbnail"`
		Duration  float64 `json:"duration"`
		URL       string  `json:"url"`
		Protocol  string  `json:"protocol"`
		Type      string  `json:"_type"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("could not read what yt-dlp resolved: %w", err)
	}
	if info.Type == "playlist" {
		return nil, errors.New("that is a set/playlist; playing those needs the proxy, which this backend does not carry yet — play a single track")
	}
	if info.URL == "" || !strings.HasPrefix(info.Protocol, "http") {
		return nil, fmt.Errorf("this source only offers a stream the speaker cannot fetch directly (%s)", info.Protocol)
	}
	artist := info.Artist
	if artist == "" {
		artist = info.Uploader
	}
	return &trackInfo{
		title:     info.Title,
		artist:    artist,
		image:     info.Thumbnail,
		duration:  info.Duration,
		streamURL: info.URL,
	}, nil
}
