package webui

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ezeql/goheos"

	"github.com/ezeql/heos-ui/position"
)

//go:embed static/index.html
var static embed.FS

// cliSearchable names the sources drivable from the CLI protocol; anything
// else present in get_music_sources would only suggest it can be used.
var cliSearchable = map[int]bool{
	1: true, 2: true, 3: true, 5: true, 6: true, 7: true, 9: true, 10: true, 1024: true,
}

// Server holds the long-lived HEOS session behind the HTTP API.
type Server struct {
	heos    *goheos.Client
	library *Library
	logger  *slog.Logger

	mu sync.Mutex
	// Times whatever we started, for streams the speaker cannot time.
	clock *position.Clock
	// Real title/artist of what we started: the firmware only ever reports
	// "Url Stream" for a URL, so this is the only place they exist.
	title  string
	artist string
	image  string
	spec   string
	// Speaker the page picked. Held here rather than in the browser so
	// every open tab agrees on what the controls are acting on.
	target string
	// The HLS conversion the speaker is reading, if any, and the port it
	// reaches this server on.
	proxy *proxied
	port  string
}

// New builds the server around an already connected goheos client.
func New(heos *goheos.Client, library *Library, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if library == nil {
		library = NewLibrary("")
	}
	return &Server{heos: heos, library: library, logger: logger}
}

// Handler returns the routes: the page and the JSON API it polls.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /stream/{name}", s.stream)
	mux.HandleFunc("GET /api/state", s.wrap(s.state))
	mux.HandleFunc("POST /api/play", s.wrap(s.play))
	mux.HandleFunc("POST /api/identify", s.wrap(s.identify))
	mux.HandleFunc("POST /api/target", s.wrap(s.targetHandler))
	mux.HandleFunc("POST /api/seek", s.wrap(s.seek))
	mux.HandleFunc("POST /api/transport", s.wrap(s.transport))
	mux.HandleFunc("POST /api/volume", s.wrap(s.volume))
	mux.HandleFunc("POST /api/mute", s.wrap(s.mute))
	mux.HandleFunc("POST /api/group", s.wrap(s.group))
	mux.HandleFunc("GET /api/playlists", s.wrap(s.playlists))
	mux.HandleFunc("POST /api/playlists/create", s.wrap(s.playlistCreate))
	mux.HandleFunc("POST /api/playlists/delete", s.wrap(s.playlistDelete))
	mux.HandleFunc("POST /api/playlists/add", s.wrap(s.playlistAdd))
	mux.HandleFunc("POST /api/playlists/remove", s.wrap(s.playlistRemove))
	mux.HandleFunc("POST /api/playlists/move", s.wrap(s.playlistMove))
	mux.HandleFunc("POST /api/playlists/play", s.wrap(s.playlistPlay))
	mux.HandleFunc("GET /api/account", s.wrap(s.account))
	mux.HandleFunc("POST /api/account/signin", s.wrap(s.accountSignin))
	mux.HandleFunc("POST /api/account/signout", s.wrap(s.accountSignout))
	// Every POST drives the speakers or the HEOS account; without this, any
	// site open in the same browser could send them. Same-origin pages and
	// non-browser clients (no Origin, no Sec-Fetch-Site) still get through.
	return http.NewCrossOriginProtection().Handler(mux)
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	// No caching: the page ships with the tool and changes with it, so a
	// browser holding an older copy would silently run stale code against a
	// newer API.
	page, err := static.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	_, _ = w.Write(page)
}

// apiError carries a status code with a user-facing reason.
type apiError struct {
	status int
	reason string
}

func (e *apiError) Error() string { return e.reason }

func badRequest(format string, args ...any) error {
	return &apiError{status: http.StatusBadRequest, reason: fmt.Sprintf(format, args...)}
}

// wrap turns an unhandled failure into a usable answer, and records it.
// Without this, anything unexpected reaches the browser as a bare 500 the
// page cannot parse.
func (s *Server) wrap(handler func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := handler(r)
		if err != nil {
			status := http.StatusInternalServerError
			var api *apiError
			switch {
			case errors.As(err, &api):
				status = api.status
			case errors.Is(err, goheos.ErrNotConnected) || errors.Is(err, goheos.ErrDisconnected):
				// Long-lived servers outlive their connection; say so rather
				// than reporting a different mystery each time.
				status = http.StatusServiceUnavailable
				err = errors.New("lost the connection to the HEOS system; it should come back on its own")
			default:
				s.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func readJSON(r *http.Request) map[string]any {
	var data map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&data)
	}
	if data == nil {
		data = map[string]any{}
	}
	return data
}

func stringField(data map[string]any, key string) string {
	if value, ok := data[key].(string); ok {
		return value
	}
	return ""
}

func numberField(data map[string]any, key string) (float64, bool) {
	value, ok := data[key].(float64)
	return value, ok
}

// snapshot pulls players and groups once for a request.
func (s *Server) snapshot(ctx context.Context) ([]playerView, []groupView, error) {
	players, err := s.heos.GetPlayers(ctx)
	if err != nil {
		return nil, nil, err
	}
	groups, err := s.heos.GetGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	return snapshotPlayers(players), snapshotGroups(groups), nil
}

func (s *Server) pinnedTarget() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target
}

// requestTarget snapshots the system and resolves the speaker a request acts
// on: one named in the request wins; otherwise whatever the page picked.
func (s *Server) requestTarget(ctx context.Context, data map[string]any) (target, []playerView, error) {
	players, groups, err := s.snapshot(ctx)
	if err != nil {
		return target{}, nil, err
	}
	spec := stringField(data, "player")
	if spec == "" {
		spec = s.pinnedTarget()
	}
	resolved, err := resolveTarget(players, groups, spec)
	if err != nil {
		return target{}, nil, badRequest("%s", err.Error())
	}
	return resolved, players, nil
}

// Serve runs the web UI until the context ends.
func (s *Server) Serve(ctx context.Context, addr string) error {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		s.port = port
	}
	defer s.stopProxy()
	server := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	//nolint:gosec // the shutdown watcher must outlive any single request
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
