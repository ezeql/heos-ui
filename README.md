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

SoundCloud metadata no longer needs a binary installed: the `soundcloud`
package speaks the private API the web player itself uses, which is where
yt-dlp reads the same fields from. See **Resolving links** below.

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

## Resolving links

`yt-dlp` is no longer required for SoundCloud, which is the only site this
backend really needs. The `soundcloud` package reaches the private API the
web player itself uses — the same one yt-dlp reads — in Go, with no
dependencies:

- `Resolve` — title, artist, artwork, description and the **full** duration
  (`full_duration`, so a preview never shortens the timeline a tracklist is
  lined up against).
- `StreamURL` — the progressive MP3. SoundCloud offers the same audio over
  HLS too, and the firmware will not follow a playlist of segments; that is
  the case the Python proxy covers and this port does not.
- `Comments` — the timed comments, paged, capped at 1000. Here `timestamp` is
  the position *within the audio*, unlike yt-dlp's `timestamp`, which is when
  the comment was posted.

yt-dlp stays as the fallback, and it is still the only route to every other
site. The order is: SoundCloud link → Go client → yt-dlp if it is installed.
When yt-dlp is absent the SoundCloud error is the one reported, because
"yt-dlp not installed" would only hide the real reason.

The private API wants a `client_id`, which SoundCloud compiles into its own
JavaScript. It is read out of the player bundles, cached in
`~/.config/heoscli/soundcloud_client_id`, and thrown away the moment the API
refuses it — that id rotates every few weeks, and being turned away is the
only way to learn it has. **This is the part that can break without warning**,
and it is why yt-dlp remains the fallback rather than being deleted.

## Finding the speaker

No command needs an IP address. `discovery.Resolve` is what every one of them
opens with, and it settles the question in the same order every time:

1. `-host` wins, because overriding is the whole point of passing it.
2. Otherwise the speaker remembered in `~/.config/heoscli/config.toml` is
   used — but only if it still answers on the HEOS port. A speaker that
   changed address, or a laptop that changed network, is exactly the case
   that used to end in a connection timeout.
3. Otherwise the network is searched, and what is found is written back to
   the config, so the next run takes the fast path again (~350ms).

The search asks for Denon's own SSDP target, which keeps every other UPnP
device on the network out of the answer, and reads each speaker's friendly
name from its UPnP description. Multicast does not survive every network —
guest VLANs, some mesh routers — so a silent search falls back to knocking on
the HEOS control port across the local /24, 128 addresses at a time.

```go
found := discovery.Search(ctx, 2*time.Second)
// [192.168.101.6 (Kitchen, Denon Home 150) 192.168.101.17 (Kitchen2, …)]
```

When several speakers answer, the first reachable one is used and the rest are
named in the log, so `-host` is one copy-paste away.

> On macOS the firewall must let the binary accept incoming connections, or
> the SSDP replies never arrive and every search falls through to the sweep.

## Development

```sh
make run    # the web UI, discovering the speaker if none is cached
make dev    # the same, rebuilt and restarted on every change (air)
make lint   # golangci-lint v2 (incl. gofumpt)
make race   # tests with the race detector
```

`make dev` watches `.html` as well as `.go`, because `index.html` is embedded
with `go:embed` and a frontend edit needs the same rebuild the backend does.
A broken build leaves the last working binary serving, so the port stays up
for as long as the syntax error takes to fix. Two things it does not cover:
`../goheos` lives outside the repo, and air reads `.air.toml` only at
startup — editing either means restarting `make dev` by hand.
