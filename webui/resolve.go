package webui

// Turning a pasted link into something the speaker can fetch.
//
// Three routes: a direct http(s) audio URL plays as-is, a SoundCloud link is
// resolved by the Go client, and anything else falls to yt-dlp when it is
// installed. A SoundCloud track with a progressive MP3 goes straight to the
// speaker; one that only offers HLS, as most do now, is converted here by
// ffmpeg (proxy.go). Sets and live streams are not played.

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

	"github.com/ezeql/heos-ui/soundcloud"
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
	hls       string  // or, when it cannot fetch anything, a playlist to convert here
}

// errNeedsFFmpeg: the track exists, but only as HLS, and converting that here
// takes ffmpeg.
var errNeedsFFmpeg = errors.New("this track is only offered as HLS, which the speaker " +
	"cannot play by itself: install ffmpeg so this machine can convert it")

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
			return nil, errors.New("that is a set/playlist; only single tracks can be played for now")
		}
		// Otherwise yt-dlp gets a turn, but only if it is installed: its
		// "not installed" would replace the real reason SoundCloud said no.
		fallback, ytdlpErr := ytdlpResolve(ctx, spec)
		if ytdlpErr != nil {
			switch {
			case errors.Is(err, errNeedsFFmpeg):
				return nil, err
			case errors.Is(err, soundcloud.ErrNoStream):
				return nil, errors.New("this track is only offered encrypted, which cannot be played here")
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
	info := &trackInfo{
		title:    track.Title,
		artist:   track.Artist,
		image:    track.Artwork,
		duration: track.Duration,
	}
	stream, err := scClient.StreamURL(ctx, track)
	if err == nil {
		info.streamURL = stream
		return info, nil
	}
	if !errors.Is(err, soundcloud.ErrNoStream) {
		return nil, err
	}
	// No MP3 to hand over: most tracks are HLS only now, which the speaker
	// cannot follow but ffmpeg can convert.
	playlist, hlsErr := scClient.HLSURL(ctx, track)
	if hlsErr != nil {
		return nil, err
	}
	if _, lookErr := exec.LookPath("ffmpeg"); lookErr != nil {
		return nil, errNeedsFFmpeg
	}
	info.hls = playlist
	return info, nil
}

// ytdlpResolve asks yt-dlp for the direct audio URL and metadata. Prefers a
// progressive http format the firmware can fetch on its own.
func ytdlpResolve(ctx context.Context, spec string) (*trackInfo, error) {
	binary := os.Getenv("HEOS_UI_YTDLP")
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
		// "--": a link starting with a dash is still a link, not a flag.
		"-J", "--", spec)
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
		return nil, errors.New("that is a set/playlist; only single tracks can be played for now")
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
