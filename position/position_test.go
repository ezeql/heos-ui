package position

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// startedAgo builds a clock that began the given seconds in the past.
func startedAgo(duration, ago float64) *Clock {
	clock := NewClock(duration)
	clock.started = time.Now().Add(-time.Duration(ago * float64(time.Second)))
	return clock
}

func TestPositionAdvancesWithTheWallClock(t *testing.T) {
	t.Parallel()
	clock := startedAgo(300, 60)
	if got := clock.Position(); got < 59.5 || got > 61 {
		t.Errorf("position = %v", got)
	}
}

func TestPositionStartsAtZero(t *testing.T) {
	t.Parallel()
	if got := NewClock(300).Position(); got >= 0.5 {
		t.Errorf("position = %v", got)
	}
}

func TestPositionIsCappedAtTheTrackLength(t *testing.T) {
	t.Parallel()
	// Past the end the bar must sit at 100%, not overflow.
	if got := startedAgo(10, 500).Position(); got != 10 {
		t.Errorf("position = %v", got)
	}
}

func TestAnUnknownDurationStillCountsUp(t *testing.T) {
	t.Parallel()
	clock := startedAgo(0, 30)
	if got := clock.Position(); got < 29 || got > 31 {
		t.Errorf("position = %v", got)
	}
}

func TestPausingFreezesThePosition(t *testing.T) {
	t.Parallel()
	clock := startedAgo(300, 60)
	clock.Sync(false)
	frozen := clock.Position()
	time.Sleep(50 * time.Millisecond)
	if got := clock.Position(); got != frozen {
		t.Errorf("position moved while paused: %v -> %v", frozen, got)
	}
}

func TestResumingDoesNotCountThePausedTime(t *testing.T) {
	t.Parallel()
	// Pause for a while and the position must not jump on resume.
	clock := startedAgo(300, 60)
	clock.Sync(false)
	atPause := clock.Position()
	time.Sleep(100 * time.Millisecond)
	clock.Sync(true)
	if got := clock.Position(); math.Abs(got-atPause) >= 0.05 {
		t.Errorf("position jumped on resume: %v -> %v", atPause, got)
	}
	if clock.pausedTotal < 90*time.Millisecond {
		t.Errorf("pausedTotal = %v", clock.pausedTotal)
	}
}

func TestRepeatedSyncCallsAreHarmless(t *testing.T) {
	t.Parallel()
	// State is polled every couple of seconds, so sync repeats constantly.
	clock := startedAgo(300, 60)
	for range 5 {
		clock.Sync(true)
	}
	if clock.pausedTotal != 0 {
		t.Errorf("pausedTotal = %v", clock.pausedTotal)
	}
	for range 5 {
		clock.Sync(false)
	}
	if clock.pausedSince.IsZero() {
		t.Error("clock not paused after sync(false)")
	}
}

func TestAnchorReplacesTheEstimate(t *testing.T) {
	t.Parallel()
	clock := startedAgo(3600, 60)
	clock.Anchor(1000)
	if got := clock.Position(); got < 999.5 || got > 1001 {
		t.Errorf("position = %v after anchor", got)
	}
}

// -- RelTime ---------------------------------------------------------------

func TestParseRelTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		xml  string
		want float64
		ok   bool
	}{
		{"<RelTime>0:05:20</RelTime>", 320, true},
		{"<RelTime>1:02:33</RelTime>", 3753, true},
		{"<RelTime> 0:00:07 </RelTime>", 7, true},
		// A stopped renderer legitimately sits at zero, indistinguishable
		// from "does not know": both fall back to the clock.
		{"<RelTime>0:00:00</RelTime>", 0, false},
		{"<RelTime>NOT_IMPLEMENTED</RelTime>", 0, false},
		{"<RelTime></RelTime>", 0, false},
		{"<nothing/>", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseRelTime(tt.xml)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseRelTime(%q) = %v/%v, want %v/%v", tt.xml, got, ok, tt.want, tt.ok)
		}
	}
}

// -- source order ----------------------------------------------------------

func TestAReportedPositionWinsAndAnchorsTheClock(t *testing.T) {
	t.Parallel()
	clock := NewClock(3600)
	got, estimated, ok := Current(context.Background(), 90_000, clock, "", true)
	if !ok || estimated || got != 90 {
		t.Errorf("current = %v/%v/%v", got, estimated, ok)
	}
	if pos := clock.Position(); pos < 89.5 || pos > 91 {
		t.Errorf("clock not anchored: %v", pos)
	}
}

func TestTheClockIsTheLastResort(t *testing.T) {
	t.Parallel()
	clock := startedAgo(3600, 120)
	got, estimated, ok := Current(context.Background(), 0, clock, "", true)
	if !ok || !estimated || got < 119 || got > 121 {
		t.Errorf("current = %v/%v/%v", got, estimated, ok)
	}
}

func TestNothingAnswers(t *testing.T) {
	t.Parallel()
	if _, _, ok := Current(context.Background(), 0, nil, "", true); ok {
		t.Error("no sources: ok must be false")
	}
}

// -- UPnP ------------------------------------------------------------------

// fakeRenderer serves a device description with a custom control path and
// answers GetPositionInfo there.
func fakeRenderer(t *testing.T, relTime string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/upnp/desc/aios_device/aios_device.xml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<service>` +
			`<serviceType>urn:schemas-upnp-org:service:AVTransport:1</serviceType>` +
			`<controlURL>/custom/control</controlURL></service>`))
	})
	mux.HandleFunc("/custom/control", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("SOAPAction") != `"urn:schemas-upnp-org:service:AVTransport:1#GetPositionInfo"` {
			http.Error(w, "bad action", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("<RelTime>" + relTime + "</RelTime>"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func TestUPnPReadsTheRendererPosition(t *testing.T) {
	t.Parallel()
	base := fakeRenderer(t, "0:10:00")
	parsed, _ := url.Parse(base)

	body, ok := soapCall(context.Background(), "http://"+parsed.Host,
		"GetPositionInfo", positionInfoBody, 5*time.Second)
	if !ok {
		t.Fatal("renderer did not answer")
	}
	got, ok := parseRelTime(body)
	if !ok || got != 600 {
		t.Errorf("position = %v/%v", got, ok)
	}
}

func TestUPnPWithNobodyListening(t *testing.T) {
	t.Parallel()
	// A port nothing listens on: the answer is "does not know", quickly.
	if _, ok := UPnP(context.Background(), "127.0.0.1", 500*time.Millisecond); ok {
		t.Error("expected no answer")
	}
}
