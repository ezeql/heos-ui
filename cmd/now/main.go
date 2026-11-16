// Command now shows which track of a set is playing on a HEOS speaker,
// live. It joins three pieces: goheos for the speaker connection
// and events, position for the playback clock, and tracklist for the cues.
//
// Usage:
//
//	now [-host IP] [-player NAME] [-offset SECONDS] <link|file>
//
// The host defaults to the one cached in
// ~/.config/heoscli/config.toml, and when that one does not answer the
// speaker is discovered on the local network instead. The spec is the set
// being played: a SoundCloud link or a local tracklist file.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/ezeql/goheos"

	"github.com/ezeql/heos-ui/discovery"
	"github.com/ezeql/heos-ui/position"
	"github.com/ezeql/heos-ui/tracklist"
)

func pickPlayer(players map[int]*goheos.Player, name string) *goheos.Player {
	var fallback *goheos.Player
	for _, p := range players {
		if name != "" && strings.EqualFold(p.Name(), name) {
			return p
		}
		if p.State() == goheos.PlayStatePlay && fallback == nil {
			fallback = p
		}
	}
	if name != "" {
		return nil
	}
	if fallback != nil {
		return fallback
	}
	for _, p := range players {
		return p
	}
	return nil
}

func main() {
	hostFlag := flag.String("host", "", "HEOS device (default: the cached host, else discovered)")
	playerName := flag.String("player", "", "player name (default: the one playing)")
	manualOffset := flag.Float64("offset", 0, "override the tracklist offset in seconds")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatalf("usage: %s [-host IP] [-player NAME] [-offset S] <link|file>", os.Args[0])
	}
	spec := flag.Arg(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	host, err := discovery.Resolve(ctx, *hostFlag, log.Printf)
	if err != nil {
		log.Fatal(err)
	}

	// The tracklist half.
	found, offset := tracklist.Find(ctx, spec,
		tracklist.WithCache(tracklist.NewCache("")))
	if found == nil {
		log.Fatal("no tracklist found for this set")
	}
	if *manualOffset != 0 {
		offset = *manualOffset
	}
	fmt.Printf("tracklist: %s, %d cues", found.Source, found.Len())
	if offset != 0 {
		fmt.Printf(", offset %+.0fs", offset)
	}
	fmt.Println()

	// The speaker half.
	c, err := goheos.Connect(ctx, host, goheos.WithAutoReconnect(time.Second, 0))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = c.Disconnect() }()
	players, err := c.GetPlayers(ctx)
	if err != nil {
		log.Fatal(err)
	}
	player := pickPlayer(players, *playerName)
	if player == nil {
		log.Fatalf("player %q not found", *playerName)
	}
	fmt.Printf("player: %s (%s)\n\n", player.Name(), player.State())

	// The position half: HEOS progress events anchor the clock when they
	// exist; the UPnP renderer covers URL streams; the clock covers the rest.
	playbackClock := position.NewClock(found.Duration)
	lastReport := time.Time{}
	lastIndex := -2 // distinct from both "no cue" (-1) and any real index
	lastLine := ""

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		playing := player.State() == goheos.PlayStatePlay
		media := player.NowPlaying()

		// Only a fresh report re-anchors; repeating a stale one would
		// freeze the estimate.
		reportedMS := 0
		if media.CurrentPosition > 0 && media.CurrentPositionUpdated.After(lastReport) {
			reportedMS = media.CurrentPosition
			lastReport = media.CurrentPositionUpdated
		}
		seconds, estimated, ok := position.Current(ctx, reportedMS, playbackClock, host, playing)
		if ok {
			at := seconds - offset
			index := -1
			if state := found.At(at); state != nil {
				index = state.Index
			}
			// The position line redraws in place; a track change gets to
			// keep its line, and a paused speaker does not repaint at all.
			if index != lastIndex && lastIndex != -2 {
				fmt.Println()
				lastLine = ""
			}
			lastIndex = index
			if line := render(found, at, estimated, playing); line != lastLine {
				fmt.Printf("\r\033[K%s", line)
				lastLine = line
			}
		}

		select {
		case <-ctx.Done():
			fmt.Println()
			return
		case <-ticker.C:
		}
	}
}

func render(found *tracklist.Tracklist, at float64, estimated, playing bool) string {
	marker := ""
	if estimated {
		marker = "~"
	}
	if !playing {
		marker += " ⏸"
	}
	state := found.At(at)
	if state == nil {
		return fmt.Sprintf("%s%s  (before the first cue)", marker, tracklist.FormatTimestamp(at))
	}
	line := fmt.Sprintf("%s%s  [%d/%d] %s", marker, tracklist.FormatTimestamp(at),
		state.Index+1, state.Total, state.Current.Describe())
	if state.Next != nil && state.RemainingKnown && state.Remaining <= 120 {
		line += fmt.Sprintf("  → %s in %s", state.Next.Describe(), tracklist.FormatTimestamp(state.Remaining))
	}
	return line
}
