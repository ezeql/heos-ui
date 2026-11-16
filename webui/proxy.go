package webui

// HLS-only tracks, played through this machine.
//
// The speaker fetches plain HTTP and will not follow an HLS playlist, and many
// SoundCloud tracks offer nothing else now. So ffmpeg converts the playlist
// into a constant-bitrate MP3 in a temporary file, about fifteen times faster
// than it plays, and the speaker reads that file from here while it is still
// being written. Constant bitrate makes the final size known before the first
// byte exists, so the speaker sees an ordinary file: a length, and byte
// ranges for seeking. This machine has to stay on while it plays.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// proxyBytesPerSecond is 192 kbit/s. Without ID3 or Xing headers a CBR stream
// is nothing but frames, so its size is the duration times this (measured: a
// 3:34 track came out 0.05 s longer than SoundCloud's duration).
const proxyBytesPerSecond = 192_000 / 8

// proxied is one conversion in progress: the file ffmpeg is writing, and how
// far it has got.
type proxied struct {
	id   string
	file *os.File
	// size is what the speaker is told: the duration at the bitrate. ffmpeg
	// tends to run a few hundredths of a second over, which are cut; if it
	// ever comes up short, the gap is served as zeros, which decoders skip
	// as noise between frames.
	size   int64
	cancel context.CancelFunc

	mu      sync.Mutex
	grew    *sync.Cond
	written int64
	done    bool
	failed  bool
}

// Write takes ffmpeg's output: append it, then wake whoever waits for it.
func (p *proxied) Write(b []byte) (int, error) {
	n, err := p.file.Write(b)
	p.mu.Lock()
	p.written += int64(n)
	p.grew.Broadcast()
	p.mu.Unlock()
	return n, err
}

func (p *proxied) finish(failed bool) {
	p.mu.Lock()
	p.done, p.failed = true, failed
	p.grew.Broadcast()
	p.mu.Unlock()
}

// stop ends the conversion and deletes its file.
func (p *proxied) stop() {
	p.cancel()
	p.finish(true)
	_ = p.file.Close()
	_ = os.Remove(p.file.Name())
}

// proxyReader is one request's view of the file: an io.ReadSeeker over the
// promised size, so http.ServeContent can answer HEAD and byte ranges.
type proxyReader struct {
	p   *proxied
	pos int64
}

func (r *proxyReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		r.pos = offset
	case io.SeekCurrent:
		r.pos += offset
	case io.SeekEnd:
		r.pos = r.p.size + offset
	}
	return r.pos, nil
}

// Read blocks until ffmpeg has written past pos: the speaker asking ahead of
// the conversion just waits for it, which takes moments at fifteen times
// real time.
func (r *proxyReader) Read(b []byte) (int, error) {
	if r.pos >= r.p.size {
		return 0, io.EOF
	}
	b = b[:min(int64(len(b)), r.p.size-r.pos)]

	p := r.p
	p.mu.Lock()
	for p.written <= r.pos && !p.done {
		p.grew.Wait()
	}
	written, failed := p.written, p.failed
	p.mu.Unlock()

	if r.pos < written {
		n, err := p.file.ReadAt(b[:min(int64(len(b)), written-r.pos)], r.pos)
		r.pos += int64(n)
		if n > 0 && errors.Is(err, io.EOF) {
			err = nil
		}
		return n, err
	}
	if failed {
		// Hours of zeros would read as a silent speaker; a cut connection
		// at least stops it.
		return 0, errors.New("the conversion stopped")
	}
	clear(b)
	r.pos += int64(len(b))
	return len(b), nil
}

// startProxy begins converting playlist and returns the id the stream is
// served under. Only the latest conversion is kept: playing something else
// is the speaker moving on.
func (s *Server) startProxy(playlist string, duration float64) (string, error) {
	if duration <= 0 {
		return "", errors.New("this track has no known length, which converting it needs")
	}
	file, err := os.CreateTemp("", "heos-ui-*.mp3")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &proxied{
		id:     strings.ToLower(rand.Text()),
		file:   file,
		size:   int64(duration * proxyBytesPerSecond),
		cancel: cancel,
	}
	p.grew = sync.NewCond(&p.mu)

	//nolint:gosec // the playlist URL comes from SoundCloud's API, not the request
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-loglevel", "error",
		"-i", playlist, "-vn", "-map_metadata", "-1",
		"-c:a", "libmp3lame", "-b:a", "192k",
		// Bare frames: a header would throw off the size, and an empty
		// length field in one can stop a hardware player.
		"-id3v2_version", "0", "-write_id3v1", "0", "-write_xing", "0",
		"-f", "mp3", "pipe:1")
	cmd.Stdout = p
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		p.stop()
		return "", fmt.Errorf("could not start ffmpeg: %w", err)
	}
	go func() {
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			s.logger.Warn("ffmpeg stopped", "error", err, "detail", strings.TrimSpace(stderr.String()))
		}
		p.finish(err != nil)
	}()

	s.mu.Lock()
	previous := s.proxy
	s.proxy = p
	s.mu.Unlock()
	if previous != nil {
		previous.stop()
	}
	return p.id, nil
}

// stopProxy ends whatever conversion is running, on shutdown.
func (s *Server) stopProxy() {
	s.mu.Lock()
	p := s.proxy
	s.proxy = nil
	s.mu.Unlock()
	if p != nil {
		p.stop()
	}
}

// stream serves a conversion to the speaker. The ".mp3" in the URL is only
// there so the firmware knows the format.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("name"), ".mp3")
	s.mu.Lock()
	p := s.proxy
	s.mu.Unlock()
	if p == nil || p.id != id {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	http.ServeContent(w, r, "", time.Time{}, &proxyReader{p: p})
}

// addressFor is this server's host:port as the speaker at speakerIP reaches
// it: the local address on the route there. With a VPN or a second network
// up, the interface list alone cannot say which one that is.
func (s *Server) addressFor(speakerIP string) (string, error) {
	local, err := LocalIP(speakerIP)
	if err != nil {
		return "", err
	}
	if s.port == "" {
		return "", errors.New("this server's own port is unknown")
	}
	return net.JoinHostPort(local, s.port), nil
}

// LocalIP is this machine's address on the route to peer: the one a device
// on that network reaches it at.
func LocalIP(peer string) (string, error) {
	// Dialing UDP sends nothing; it only asks the kernel for the route.
	conn, err := net.Dial("udp4", net.JoinHostPort(peer, "1255"))
	if err != nil {
		return "", fmt.Errorf("no route to %s: %w", peer, err)
	}
	defer func() { _ = conn.Close() }()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", errors.New("no local address on the route")
	}
	return local.IP.String(), nil
}

// streamFor is what to hand the speaker: the link itself, or, for an HLS
// playlist, the address of its conversion here.
func (s *Server) streamFor(info *trackInfo, playerID int, players []playerView) (string, error) {
	if info.hls == "" {
		return info.streamURL, nil
	}
	player, ok := findPlayer(players, playerID)
	if !ok || player.IP == "" {
		return "", badRequest("unknown speaker address")
	}
	// The address first: it is cheap, and a conversion nobody can reach
	// would only spin ffmpeg for nothing.
	address, err := s.addressFor(player.IP)
	if err != nil {
		return "", err
	}
	id, err := s.startProxy(info.hls, info.duration)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s/stream/%s.mp3", address, id), nil
}
