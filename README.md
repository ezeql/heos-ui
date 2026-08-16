# heoscli-go

Go port of [heoscli](../heoscli), starting with the **tracklist** backend:
knowing which track of a DJ set is playing.

A set streamed through the proxy reaches the speaker as a single hour-long URL
stream, so the firmware reports "Url Stream" and nothing else for the whole
hour. This package finds a tracklist for the set, lines it up with the audio,
and maps a playback position to the track on right now.

## What is ported

- **Model** — `Cue`, `CueState`, `Tracklist.At(position)` (bisect lookup).
- **Parser** — cue points out of free text: descriptions, pasted lists. The
  central rule survives the port: a timestamp only counts when it opens the
  line, which is what keeps "recorded live at 2:00 AM" out.
- **Merge** — folding newly identified cues into a tracklist, with source
  names deduplicated (`soundcloud-comments+shazam`, never `shazam+shazam+…`).
- **Providers** — local text file, SoundCloud description, SoundCloud timed
  comments, chained cheapest-and-most-trustworthy first. Nothing in the chain
  fails loudly: no tracklist is an answer, not an error.
- **Cache** — one JSON file, atomic writes, 30-day TTL, byte-compatible with
  the Python implementation: both can share
  `~/.config/heoscli/tracklists.json` (verified round-trip in both
  directions).
- **Alignment** — sliding the tracklist against the density of timed comments
  to recover a trimmed intro, refusing to invent a shift when the comments
  say nothing (`EstimateOffset`).

SoundCloud metadata comes from executing the `yt-dlp` binary (`yt-dlp -J`);
without it installed those providers quietly yield nothing, like any other
source that is down.

## Deliberately not ported

- **Acoustic identification** (`acoustic.py`, Shazam sampling,
  `scan_audio`/`identify_playing`) — non-trivial; the `shazam` source name is
  still honored when merging and in cached entries.
- **1001tracklists scraping** (`scrape.py`) — needs browser cookies. The
  `Provider` interface is the extension point when it becomes worth it.
- The CLI/TUI/web frontends: this is the backend only.

## Usage

```go
cache := tracklist.NewCache("") // default: ~/.config/heoscli/tracklists.json

found, offset := tracklist.Find(ctx, "https://soundcloud.com/dj/set",
    tracklist.WithCache(cache))
if found == nil {
    return // a set with no tracklist looks exactly like before
}

state := found.At(position - offset)
if state != nil {
    fmt.Println(state.Current.Describe())
}
```

## Development

```sh
make lint   # golangci-lint v2 (incl. gofumpt)
make race   # tests with the race detector
```
