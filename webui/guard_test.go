package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = "abcdefghijklmnopqrstuvwxyz"

// serveAs sends one request as if it came from remoteAddr, naming host.
func serveAs(server *Server, method, host, path, remoteAddr string, body io.Reader, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://"+host+path, body)
	request.RemoteAddr = remoteAddr
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestWhoGetsIn(t *testing.T) {
	t.Parallel()
	const (
		loopback = "127.0.0.1:50000"
		phone    = "192.168.1.20:50000"
	)
	good := &http.Cookie{Name: keyCookie, Value: testKey}
	bad := &http.Cookie{Name: keyCookie, Value: "guessed"}
	tests := []struct {
		name   string
		key    string
		host   string
		path   string
		from   string
		cookie *http.Cookie
		want   int
	}{
		{"this machine", testKey, "localhost:8080", "/api/playlists", loopback, nil, http.StatusOK},
		{"this machine by IP", testKey, "127.0.0.1:8080", "/api/playlists", loopback, nil, http.StatusOK},
		// DNS rebinding: a page whose domain now resolves to 127.0.0.1.
		{"a rebound domain", testKey, "evil.example:8080", "/api/playlists", loopback, nil, http.StatusForbidden},
		{"a phone without the key", testKey, "192.168.1.10:8080", "/api/playlists", phone, nil, http.StatusUnauthorized},
		{"a phone with a wrong cookie", testKey, "192.168.1.10:8080", "/api/playlists", phone, bad, http.StatusUnauthorized},
		{"a phone with the cookie", testKey, "192.168.1.10:8080", "/api/playlists", phone, good, http.StatusOK},
		{"a phone with a wrong key", testKey, "192.168.1.10:8080", "/?key=guessed", phone, nil, http.StatusUnauthorized},
		{"a phone when no key was set", "", "192.168.1.10:8080", "/api/playlists", phone, good, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := New(nil, testLibrary(t), nil)
			server.SetKey(tt.key)
			if got := serveAs(server, http.MethodGet, tt.host, tt.path, tt.from, nil, tt.cookie); got.Code != tt.want {
				t.Errorf("status = %d, want %d (%s)", got.Code, tt.want, strings.TrimSpace(got.Body.String()))
			}
		})
	}
}

// The link with the key sets the cookie and sends the phone on without it in
// the address bar; the cookie alone gets it in after that.
func TestTheLinkLetsAPhoneIn(t *testing.T) {
	t.Parallel()
	server := New(nil, testLibrary(t), nil)
	server.SetKey(testKey)

	got := serveAs(server, http.MethodGet, "192.168.1.10:8080", "/?key="+testKey, "192.168.1.20:50000", nil, nil)
	if got.Code != http.StatusSeeOther || got.Header().Get("Location") != "/" {
		t.Fatalf("status = %d, location %q", got.Code, got.Header().Get("Location"))
	}
	cookies := got.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != testKey || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookies = %+v", cookies)
	}
	if got := serveAs(server, http.MethodGet, "192.168.1.10:8080", "/", "192.168.1.20:50000", nil, cookies[0]); got.Code != http.StatusOK {
		t.Errorf("page with the cookie = %d", got.Code)
	}
}

func TestAnOversizedBodyIsRefused(t *testing.T) {
	t.Parallel()
	library := testLibrary(t)
	server := New(nil, library, nil)
	body := `{"name":"` + strings.Repeat("x", maxBody) + `"}`
	got := serveAs(server, http.MethodPost, "127.0.0.1:8080", "/api/playlists/create", "127.0.0.1:50000",
		strings.NewReader(body), nil)
	if got.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", got.Code)
	}
	if len(library.All()) != 0 {
		t.Error("a playlist was created anyway")
	}
}

// The speaker sends no cookie and nobody knows what Host its firmware puts
// in: the stream must reach it regardless.
func TestTheSpeakerStillGetsItsStream(t *testing.T) {
	fakeFFmpeg(t, "ABCD", 0)
	server := New(nil, testLibrary(t), nil)
	server.SetKey(testKey)
	t.Cleanup(server.stopProxy)
	id, err := server.startProxy("https://example.com/playlist.m3u8", 1)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/stream/"+id+".mp3", nil)
	request.Host = ""
	request.RemoteAddr = "192.168.1.11:50000"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Body.String(), "ABCD") {
		t.Errorf("status = %d, body starts %q", recorder.Code, recorder.Body.String()[:min(4, recorder.Body.Len())])
	}
}

func TestThisMachine(t *testing.T) {
	t.Parallel()
	const hostname = "Studio-Mac.local"
	for host, want := range map[string]bool{
		"localhost:8080":        true,
		"127.0.0.1:8080":        true,
		"[::1]:8080":            true,
		"192.168.1.10":          true,
		"studio-mac.local:8080": true,
		"studio-mac:8080":       true,
		"evil.example:8080":     false,
		"studio-mac.evil.local": false,
		"":                      false,
	} {
		if got := thisMachine(host, hostname); got != want {
			t.Errorf("thisMachine(%q) = %v, want %v", host, got, want)
		}
	}
}

// The key is made once and kept, so a phone let in stays in after a restart.
func TestTheKeyOutlivesARestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	first, err := LoadKey()
	if err != nil || len(first) < 20 {
		t.Fatalf("key = %q, %v", first, err)
	}
	second, err := LoadKey()
	if err != nil || second != first {
		t.Errorf("second key = %q, want %q (%v)", second, first, err)
	}
	info, err := os.Stat(filepath.Join(home, ".config", "heoscli", "heos-ui.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("key file: %v, %v", info, err)
	}
}
