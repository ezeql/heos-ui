package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ezeql/heos-ui/position"
)

// Every open tab polls /api/state on its own goroutine while seek
// re-anchors the same clock.
func TestClockAcrossConcurrentRequests(t *testing.T) {
	t.Parallel()
	s := &Server{}
	if _, ok := s.clockReading(true); ok {
		t.Error("read a clock before anything was played")
	}
	s.clock = position.NewClock(0)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			// Alternating play/pause makes every Sync write.
			if _, ok := s.clockReading(i%2 == 0); !ok {
				t.Error("clock missing")
			}
			s.anchorClock(float64(i))
		})
	}
	wg.Wait()

	s.anchorClock(60)
	if ms, _ := s.clockReading(false); ms < 60_000 || ms > 61_000 {
		t.Errorf("position after seeking to 60s = %dms", ms)
	}
}

// The API drives the speakers and the HEOS account, so a page on some other
// site must not be able to use it through the browser of whoever runs the UI.
func TestCrossSiteRequestsAreRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"a page on another site", map[string]string{"Origin": "https://elsewhere.example"}, http.StatusForbidden},
		{"the browser flags it cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"the UI's own page", map[string]string{"Origin": "http://127.0.0.1:8080"}, http.StatusOK},
		{"curl", nil, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			library := testLibrary(t)
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/playlists/create",
				strings.NewReader(`{"name":"Fiesta"}`))
			request.RemoteAddr = "127.0.0.1:50000"
			for key, value := range tt.headers {
				request.Header.Set(key, value)
			}
			recorder := httptest.NewRecorder()
			New(nil, library, nil).Handler().ServeHTTP(recorder, request)

			if recorder.Code != tt.want {
				t.Errorf("status = %d, want %d (%s)", recorder.Code, tt.want, recorder.Body)
			}
			if created := len(library.All()) == 1; created != (tt.want == http.StatusOK) {
				t.Errorf("playlist created = %v", created)
			}
		})
	}
}
