package tracklist

// Metadata extraction by shelling out to yt-dlp.
//
// The binary runs as a subprocess. When it is not installed the providers
// simply come back empty, which the chain treats like any other source that
// is down.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// mediaInfo is the slice of the yt-dlp info dict this package reads.
type mediaInfo struct {
	Description string         `json:"description"`
	Duration    float64        `json:"duration"`
	Comments    []mediaComment `json:"comments"`
}

// mediaComment is a timed comment. StartTime is the position within the
// track; yt-dlp's "timestamp" is the date the comment was posted, which is a
// different thing entirely.
type mediaComment struct {
	StartTime *float64 `json:"start_time"`
	Text      string   `json:"text"`
}

// extractor resolves a spec to its metadata. Swapped out in tests.
type extractor func(ctx context.Context, spec string, withComments bool) (*mediaInfo, error)

func ytdlpExtract(ctx context.Context, spec string, withComments bool) (*mediaInfo, error) {
	// HEOSCLI_YTDLP points at a specific binary, e.g. one inside a
	// virtualenv; otherwise PATH decides.
	binary := os.Getenv("HEOSCLI_YTDLP")
	if binary == "" {
		found, err := exec.LookPath("yt-dlp")
		if err != nil {
			return nil, fmt.Errorf("yt-dlp not installed: %w", err)
		}
		binary = found
	}
	args := []string{"--no-download", "--no-warnings", "-J"}
	if withComments {
		args = append(args, "--write-comments")
	}
	// "--": a link starting with a dash is still a link, not a flag.
	args = append(args, "--", spec)

	//nolint:gosec // the binary name is fixed; the spec is the user's own link
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		reason, _, _ := strings.Cut(stderr.String(), "\n")
		return nil, fmt.Errorf("yt-dlp: %w: %s", err, reason)
	}
	var info mediaInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("yt-dlp output: %w", err)
	}
	return &info, nil
}

// commentTimes returns the offsets of every timed comment, for aligning a
// tracklist against them.
func commentTimes(comments []mediaComment) []float64 {
	var times []float64
	for _, comment := range comments {
		if comment.StartTime != nil && *comment.StartTime >= 0 {
			times = append(times, *comment.StartTime)
		}
	}
	return times
}
