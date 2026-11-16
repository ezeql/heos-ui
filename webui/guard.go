package webui

// Who may use the page and the API.
//
// The server has to listen on the network: the speaker fetches converted
// tracks from it, and a phone is a reason to run it at all. So the network
// gets in with a key, and this machine without one:
//
//   - The Host header must name this machine: an IP address, localhost, or
//     its own hostname. A page that points its own domain at 127.0.0.1 (DNS
//     rebinding) still arrives with that domain as its Host, so this check
//     is what makes letting loopback in safe.
//   - Another device presents the key once, as ?key= in the link heos-ui
//     prints when it starts, and keeps it in a cookie after that.
//   - Bodies are capped. Every request here is a few small JSON fields.
//
// /stream/ stays outside all of it: the speaker sends no cookie, and the id
// in the path is already unguessable.

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/ezeql/heos-ui/internal/config"
)

const (
	keyCookie = "heos-ui-key"
	// maxBody is far beyond any request the page makes.
	maxBody = 64 << 10
)

// LoadKey returns the key other devices need, creating it on first use. It is
// kept next to the rest of the state so a phone, once let in, stays in
// across restarts.
func LoadKey() (string, error) {
	path := config.Path("heos-ui.key")
	//nolint:gosec // the path is this tool's own key file, never user input
	if raw, err := os.ReadFile(path); err == nil {
		if key := strings.TrimSpace(string(raw)); len(key) >= 20 {
			return key, nil
		}
	}
	key := strings.ToLower(rand.Text())
	if err := config.WriteAtomic(path, []byte(key+"\n")); err != nil {
		return "", err
	}
	return key, nil
}

// SetKey lets other devices in with key. Without one, only this machine is
// served.
func (s *Server) SetKey(key string) {
	s.key = key
}

// guard applies the rules above to everything but /stream/.
func (s *Server) guard(next http.Handler) http.Handler {
	hostname, _ := os.Hostname()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !thisMachine(r.Host, hostname) {
			http.Error(w, "this server only answers to its own address", http.StatusForbidden)
			return
		}
		if !s.letIn(w, r) {
			return
		}
		// Read the body here, capped, so an oversized one is refused outright
		// instead of reaching a handler as an empty request.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "could not read the request", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

// letIn reports whether r may go on, answering it when it may not.
func (s *Server) letIn(w http.ResponseWriter, r *http.Request) bool {
	if fromLoopback(r.RemoteAddr) {
		return true
	}
	if s.key == "" {
		http.Error(w, "this server only answers the machine it runs on", http.StatusForbidden)
		return false
	}
	if given := r.URL.Query().Get("key"); given != "" {
		if !sameKey(given, s.key) {
			http.Error(w, "wrong key", http.StatusUnauthorized)
			return false
		}
		// No Secure flag: the page is plain HTTP on the local network, and a
		// Secure cookie would never be sent back.
		//nolint:gosec // see above
		http.SetCookie(w, &http.Cookie{
			Name: keyCookie, Value: s.key, Path: "/",
			MaxAge: 400 * 24 * 60 * 60, HttpOnly: true,
			// Lax, not Strict: the link is opened from other apps, and a
			// Strict cookie can be dropped on the redirect that follows.
			SameSite: http.SameSiteLaxMode,
		})
		// Take the key out of the address bar and the history.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return false
	}
	if cookie, err := r.Cookie(keyCookie); err == nil && sameKey(cookie.Value, s.key) {
		return true
	}
	http.Error(w, "open the link heos-ui printed when it started (it ends in ?key=…) once on this device",
		http.StatusUnauthorized)
	return false
}

func sameKey(given, key string) bool {
	return subtle.ConstantTimeCompare([]byte(given), []byte(key)) == 1
}

func fromLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// thisMachine reports whether a Host header names this machine rather than
// some domain that happens to resolve to it.
func thisMachine(hostport, hostname string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" {
		return false
	}
	if host == "localhost" || net.ParseIP(host) != nil {
		return true
	}
	name := strings.TrimSuffix(strings.ToLower(hostname), ".local")
	return name != "" && (host == name || host == name+".local")
}
