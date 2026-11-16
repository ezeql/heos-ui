# heos-ui

A command-line tool and web UI for Denon/HEOS speakers, built on
[goheos](https://github.com/ezeql/goheos), the Go library for the HEOS protocol
that does all the talking to the speakers. The web UI plays and controls; the
commands know which track of a DJ set is playing.

State lives in `~/.config/heoscli/`, in the same files and formats the Python
[heoscli](https://github.com/ezeql/heoscli) uses, so both tools can share it.

## Install

```sh
brew install ezeql/personal/heos-ui
heos-ui    # then open http://localhost:8080
```

On a phone or another computer, open the link `heos-ui` prints when it
starts, once (see **Web UI** for why).

The formula installs the three commands below as `heos-ui`, `heos-now` and
`heos-tracklist`, with ffmpeg, which most SoundCloud tracks now need (see
**Web UI**). yt-dlp is optional: `brew install yt-dlp` for links from other
sites. From source, `go install github.com/ezeql/heos-ui/cmd/...@latest`
installs them as `ui`, `now` and `tracklist`.

On macOS, two settings decide whether it works at all:

- **Local Network** (System Settings → Privacy & Security): allow the terminal
  you run it from, or every connection to a speaker fails with "no route to
  host" and discovery finds nothing.
- **Firewall**, when it is on: allow incoming connections for `heos-ui`. The
  speaker fetches converted SoundCloud tracks from it.

## Commands

| Command | What it does |
|---|---|
| `ui [-host IP] [-port 8080]` | The web UI (`make run`). See **Web UI**. |
| `now [-host IP] [-player NAME] [-offset S] <link\|file>` | Which track of a set is playing on a speaker, live. |
| `tracklist [-at 1:02:30] [-refresh] [-no-cache] [-v] <link\|file>` | Finds a tracklist for a set and, with `-at`, what plays at that position. |

All three live under `cmd/`. `ui` and `now` need no IP address: the speaker is
found on the network when none is cached (see **Finding the speaker**). For
`now` and `tracklist`, the spec is a SoundCloud link or a local text file
with a tracklist in it.

## Web UI

The `webui` package serves one embedded page (`webui/static/index.html`) and
the JSON API it polls, talking to the speakers through goheos:

- **Playback**: a direct http(s) audio URL plays as-is, a SoundCloud link is
  resolved by the Go client, and anything else goes to yt-dlp if it is
  installed. Then play, pause, stop, next and previous.
- **HLS-only tracks**: most SoundCloud tracks no longer offer an MP3, only
  HLS, which the speaker will not follow. ffmpeg converts those on this
  machine into a constant-bitrate MP3, about fifteen times faster than it
  plays, and the server hands it to the speaker as an ordinary file under
  `/stream/` on the same port: known length, byte ranges, so seeking works.
  This needs ffmpeg on `PATH`, this machine on for as long as the track
  plays, and the macOS firewall letting the speaker connect in.
- **Speakers**: pick the target, set the volume, mute, and group or ungroup.
- **Position and seek**: the position comes from HEOS when it reports one,
  from the speaker's UPnP renderer otherwise (it answers even for URL
  streams), and from a clock started at play as the last resort. Seeking goes
  over UPnP, because the HEOS protocol has no seek at all.
- **Playlists**: this tool's own lists of links, unrelated to HEOS playlists,
  in `~/.config/heoscli/playlists.json`. Same file and schema as the Python
  tool, so both can share it.
- **HEOS account**: sign in and out.

The server listens on every interface, because phones on the network are a
reason to run it and the speaker fetches converted tracks from it. Who gets in:

- **This machine** is served as is.
- **Any other device** needs the key, once. `heos-ui` prints the link at
  startup (`http://<this machine>:8080/?key=…`); opening it sets a cookie and
  drops the key from the address bar. The key lives in
  `~/.config/heoscli/heos-ui.key`; delete that file and restart to lock every
  device out again.
- **The `Host` header** has to name this machine: an IP address, `localhost`
  or its own hostname. A site that points its domain at your machine (DNS
  rebinding) is refused, which is what makes letting this machine in without
  a key safe.
- **Other sites** open in the same browser can't send requests (403).
- **Request bodies** over 64 KiB are refused (413).
- **`/stream/`**, which the speaker reads, is the one path outside all of
  this: the speaker sends no cookie, and each stream's id is unguessable.

## Tracklists

A DJ set reaches the speaker as a single hour-long URL stream, so the
firmware reports "Url Stream" and nothing else for the whole hour. The `tracklist` package finds a tracklist for the set, lines it up with
the audio, and maps a playback position to the track on right now.

- **Model** — `Cue`, `CueState`, `Tracklist.At(position)` (bisect lookup).
- **Parser** — cue points out of free text: descriptions, pasted lists. The
  central rule: a timestamp only counts when it opens the line, which is what keeps "recorded live at 2:00 AM" out.
- **Providers** — local text file, SoundCloud description, SoundCloud timed
  comments, chained cheapest-and-most-trustworthy first. Nothing in the chain
  fails loudly: no tracklist is an answer, not an error.
- **Cache** — one JSON file, atomic writes, 30-day TTL, byte-compatible with
  the Python heoscli: both can share
  `~/.config/heoscli/tracklists.json` (verified round-trip in both
  directions).
- **Alignment** — sliding the tracklist against the density of timed comments
  to recover a trimmed intro, refusing to invent a shift when the comments
  say nothing (`EstimateOffset`).

SoundCloud metadata needs no binary installed: the `soundcloud`
package speaks the private API the web player itself uses, which is where
yt-dlp reads the same fields from. See **Resolving links** below.

### Usage

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

`yt-dlp` is not required for SoundCloud, which is the only site this tool
really needs. The `soundcloud` package reaches the private API the
web player itself uses — the same one yt-dlp reads — in Go, with no
dependencies:

- `Resolve` — title, artist, artwork, description and the **full** duration
  (`full_duration`, so a preview never shortens the timeline a tracklist is
  lined up against).
- `StreamURL` — the progressive MP3, when the track still has one.
- `HLSURL` — the HLS playlist, which is all most tracks offer now. The
  firmware will not follow it, so the web UI converts it with ffmpeg.
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

## Not supported

- **Identifying tracks by listening** (Shazam-style sampling): in the web UI,
  "identify" answers 501. The `shazam` source name is still honored in cache
  entries the Python heoscli wrote.
- **1001tracklists** as a tracklist source: it needs browser cookies. The
  `Provider` interface is the extension point when it becomes worth it.
- **Sets, live streams, and feeding a playlist one track after another**:
  only single tracks play, and "play" on a playlist plays its first track.
- **The audio quality line** in the web UI (the DIDL parse): it shows no line,
  like a speaker that does not answer.
- **Controlling playback from the command line**: `now` and `tracklist` only
  read; play, volume, grouping and seek are the web UI's.

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
> `go run` (and so `make run`) builds a new binary every time, which the
> firewall has never approved. To test discovery itself, build to a fixed
> path (`go build -o bin/ui ./cmd/ui`) and approve that one once.

## Development

```sh
make run    # the web UI, discovering the speaker if none is cached
make dev    # the same, rebuilt and restarted on every change (air)
make test   # go test ./...
make race   # tests with the race detector
make lint   # golangci-lint v2 (CI pins v2.14)
make fmt    # gofumpt + goimports
make cover  # coverage, as CI measures it
make vuln   # govulncheck
make tidy   # go mod tidy, failing if go.mod changes
```

`make dev` watches `.html` as well as `.go`, because `index.html` is embedded
with `go:embed` and a frontend edit needs the same rebuild the backend does.
A broken build leaves the last working binary serving, so the port stays up
for as long as the syntax error takes to fix. Two things it does not cover:
`../goheos` lives outside the repo, and air reads `.air.toml` only at
startup — editing either means restarting `make dev` by hand.

`go.mod` pins a published [goheos](https://github.com/ezeql/goheos). To build
against a local checkout next to this repo instead, create a workspace, which
git ignores:

```sh
go work init . ../goheos
```

## License

Apache License 2.0; see [LICENSE](LICENSE).
