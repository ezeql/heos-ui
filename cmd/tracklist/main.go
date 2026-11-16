// Command tracklist exercises the tracklist backend from the terminal: find
// a tracklist for a set and, optionally, answer what is playing at a given
// position.
//
// Usage:
//
//	tracklist [-at 1:02:30] [-refresh] [-no-cache] [-v] <link|file>
//
// The spec is a SoundCloud link (resolved by the Go client, with yt-dlp as
// the fallback when it is installed) or a local text file with a tracklist
// in it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/ezeql/heos-ui/tracklist"
)

func main() {
	at := flag.String("at", "", "position to look up, as seconds or mm:ss / h:mm:ss")
	refresh := flag.Bool("refresh", false, "bypass the cache read")
	noCache := flag.Bool("no-cache", false, "do not read or write the shared cache")
	verbose := flag.Bool("v", false, "log skipped providers and quiet failures")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatalf("usage: %s [-at POS] [-refresh] [-no-cache] [-v] <link|file>", os.Args[0])
	}
	spec := flag.Arg(0)

	opts := []tracklist.Option{}
	if !*noCache {
		opts = append(opts, tracklist.WithCache(tracklist.NewCache("")))
	}
	if *refresh {
		opts = append(opts, tracklist.WithRefresh())
	}
	if *verbose {
		opts = append(opts, tracklist.WithLogger(
			slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	found, offset := tracklist.Find(ctx, spec, opts...)
	if found == nil {
		fmt.Println("no tracklist found (a set with no tracklist looks exactly like this)")
		return
	}

	fmt.Printf("source: %s", found.Source)
	if found.Duration > 0 {
		fmt.Printf("  duration: %s", tracklist.FormatTimestamp(found.Duration))
	}
	if offset != 0 {
		fmt.Printf("  offset: %+.0fs", offset)
	}
	fmt.Printf("  cues: %d\n\n", found.Len())

	for i, cue := range found.Cues {
		fmt.Printf("%3d. %8s  %s\n", i+1, tracklist.FormatTimestamp(cue.Start), cue.Describe())
	}

	if *at == "" {
		return
	}
	position, ok := tracklist.ParseTimestamp(*at)
	if !ok {
		seconds, err := strconv.ParseFloat(*at, 64)
		if err != nil {
			log.Fatalf("cannot read position %q", *at)
		}
		position = seconds
	}
	state := found.At(position - offset)
	fmt.Println()
	if state == nil {
		fmt.Printf("at %s: nothing yet (before the first cue)\n", tracklist.FormatTimestamp(position))
		return
	}
	fmt.Printf("at %s: [%d/%d] %s  (%s in", tracklist.FormatTimestamp(position),
		state.Index+1, state.Total, state.Current.Describe(), tracklist.FormatTimestamp(state.Elapsed))
	if state.RemainingKnown {
		fmt.Printf(", %s left", tracklist.FormatTimestamp(state.Remaining))
	}
	fmt.Println(")")
	if state.Next != nil {
		fmt.Printf("next: %s\n", state.Next.Describe())
	}
}
