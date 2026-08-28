package soundcloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testID = "abcdefghijklmnopqrstuvwxyz012345"

// serve stands in for the API, with a client that already holds an id so
// nothing reaches the real SoundCloud.
func serve(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{
		http:     &http.Client{Timeout: 5 * time.Second},
		base:     server.URL,
		home:     server.URL,
		clientID: testID,
	}
}

func trackJSON(extra string) string {
	return `{"kind":"track","id":189305064,"title":"Solomun Boiler Room",` +
		`"description":"00:00 First\n05:00 Second","duration":7286766,` +
		`"artwork_url":"https://i1.sndcdn.com/artworks-000110085049-large.jpg",` +
		`"permalink_url":"https://soundcloud.com/platform/solomun",` +
		`"user":{"username":"Boiler Room"},` +
		`"media":{"transcodings":[` +
		`{"url":"URL/hls","preset":"mp3_0_1","format":{"protocol":"hls","mime_type":"audio/mpeg"}},` +
		`{"url":"URL/progressive","preset":"mp3_0_1","format":{"protocol":"progressive","mime_type":"audio/mpeg"}}` +
		`]}` + extra + `}`
}

func TestALinkIsRecognized(t *testing.T) {
	yes := []string{
		"https://soundcloud.com/platform/solomun",
		"https://m.soundcloud.com/platform/solomun",
		"https://on.soundcloud.com/abc123",
	}
	for _, link := range yes {
		if !Is(link) {
			t.Errorf("Is(%q) = false, want true", link)
		}
	}
	no := []string{
		"https://youtube.com/watch?v=1",
		"https://notsoundcloud.com/x",
		"https://example.com/soundcloud.com/x",
		"not a url at all",
	}
	for _, link := range no {
		if Is(link) {
			t.Errorf("Is(%q) = true, want false", link)
		}
	}
}

func TestATrackResolves(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("client_id") != testID {
			t.Errorf("the call carried client_id %q", r.URL.Query().Get("client_id"))
		}
		_, _ = fmt.Fprint(w, trackJSON(""))
	})

	track, err := client.Resolve(context.Background(), "https://soundcloud.com/platform/solomun")
	if err != nil {
		t.Fatal(err)
	}
	if track.ID != 189305064 {
		t.Errorf("ID = %d", track.ID)
	}
	if track.Artist != "Boiler Room" {
		t.Errorf("Artist = %q, want the uploader", track.Artist)
	}
	if track.Duration != 7286.766 {
		t.Errorf("Duration = %v, want seconds, not milliseconds", track.Duration)
	}
	if !strings.Contains(track.Artwork, "t500x500") {
		t.Errorf("Artwork = %q, want the large variant", track.Artwork)
	}
}

// publisher_metadata names the actual artist; the uploader is only the
// fallback for tracks that carry none.
func TestThePublishedArtistWins(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, trackJSON(`,"publisher_metadata":{"artist":"Solomun"}`))
	})
	track, err := client.Resolve(context.Background(), "https://soundcloud.com/x/y")
	if err != nil {
		t.Fatal(err)
	}
	if track.Artist != "Solomun" {
		t.Errorf("Artist = %q, want Solomun", track.Artist)
	}
}

// A preview shortens duration while full_duration keeps the real length; a
// tracklist lined up against the preview would be wrong after the first cue.
func TestTheFullDurationWins(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, trackJSON(`,"full_duration":7286766`))
	})
	client2 := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, strings.Replace(trackJSON(`,"full_duration":7286766`),
			`"duration":7286766`, `"duration":30000`, 1))
	})
	for _, c := range []*Client{client, client2} {
		track, err := c.Resolve(context.Background(), "https://soundcloud.com/x/y")
		if err != nil {
			t.Fatal(err)
		}
		if track.Duration != 7286.766 {
			t.Errorf("Duration = %v, want the full length", track.Duration)
		}
	}
}

func TestAPlaylistIsNotATrack(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"kind":"playlist","id":7,"title":"A set"}`)
	})
	_, err := client.Resolve(context.Background(), "https://soundcloud.com/x/sets/y")
	if !errors.Is(err, ErrNotATrack) {
		t.Errorf("err = %v, want ErrNotATrack", err)
	}
}

func TestTheProgressiveStreamIsChosen(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/progressive") {
			_, _ = fmt.Fprint(w, `{"url":"https://cf-media.sndcdn.com/x.128.mp3?Policy=y"}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/hls") {
			t.Error("the HLS transcoding was asked for; the speaker cannot follow it")
		}
		_, _ = fmt.Fprint(w, strings.ReplaceAll(trackJSON(""), "URL", "http://"+r.Host))
	})

	track, err := client.Resolve(context.Background(), "https://soundcloud.com/x/y")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.StreamURL(context.Background(), track)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stream, "https://cf-media.sndcdn.com/") {
		t.Errorf("stream = %q", stream)
	}
}

// Some tracks are HLS only, which the firmware will not follow.
func TestATrackWithoutAProgressiveStreamSaysSo(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"kind":"track","id":1,"title":"x","duration":1000,`+
			`"media":{"transcodings":[{"url":"u","format":{"protocol":"hls","mime_type":"audio/mpeg"}}]}}`)
	})
	track, err := client.Resolve(context.Background(), "https://soundcloud.com/x/y")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.StreamURL(context.Background(), track); !errors.Is(err, ErrNoStream) {
		t.Errorf("err = %v, want ErrNoStream", err)
	}
}

// A snipped transcoding is a 30-second preview; the full one is preferred
// whatever order they arrive in.
func TestAFullStreamBeatsASnippet(t *testing.T) {
	all := []transcoding{
		{URL: "snip", Snipped: true},
		{URL: "full"},
	}
	for i := range all {
		all[i].Format.Protocol = "progressive"
	}
	chosen, ok := progressive(all)
	if !ok || chosen.URL != "full" {
		t.Errorf("chose %q, want full", chosen.URL)
	}
	if chosen, ok := progressive(all[:1]); !ok || chosen.URL != "snip" {
		t.Errorf("a snippet alone should still be offered, got %q", chosen.URL)
	}
}

func TestTimedCommentsComeBackInSeconds(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"collection":[`+
			`{"body":"Solomun - Kackvogel","timestamp":1927000},`+
			`{"body":"no timestamp here","timestamp":null},`+
			`{"body":"later","timestamp":7023000}`+
			`],"next_href":""}`)
	})
	comments, err := client.Comments(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments, want the 2 timed ones", len(comments))
	}
	if comments[0].At != 1927 {
		t.Errorf("At = %v, want 1927 seconds", comments[0].At)
	}
}

func TestCommentsFollowTheNextPage(t *testing.T) {
	var calls atomic.Int32
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			_, _ = fmt.Fprintf(w, `{"collection":[{"body":"one","timestamp":1000}],`+
				`"next_href":"http://%s/tracks/1/comments?offset=1"}`, r.Host)
		default:
			_, _ = fmt.Fprint(w, `{"collection":[{"body":"two","timestamp":2000}],"next_href":""}`)
		}
	})
	comments, err := client.Comments(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments, want both pages", len(comments))
	}
	if comments[1].Text != "two" {
		t.Errorf("second comment = %q", comments[1].Text)
	}
}

// A page that fails after some comments were already read keeps what it has:
// fewer comments make a worse tracklist, not a broken one.
func TestAFailedPageKeepsWhatWasRead(t *testing.T) {
	var calls atomic.Int32
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = fmt.Fprintf(w, `{"collection":[{"body":"one","timestamp":1000}],`+
				`"next_href":"http://%s/tracks/1/comments?offset=1"}`, r.Host)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	comments, err := client.Comments(context.Background(), 1)
	if err != nil {
		t.Fatalf("a partial read should not fail: %v", err)
	}
	if len(comments) != 1 {
		t.Errorf("got %d comments, want the one that was read", len(comments))
	}
}

// The id rotates. Being refused is how that is discovered, and the only
// correct answer is to go get a new one and try once more.
func TestARefusedIDIsReplaced(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const fresh = "FRESHfghijklmnopqrstuvwxyz012345"
	var calls atomic.Int32
	client := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/player.js"):
			_, _ = fmt.Fprintf(w, `x={client_id:"%s"};`, fresh)
		case r.URL.Path == "/":
			_, _ = fmt.Fprintf(w, `<script src="http://%s/player.js"></script>`, r.Host)
		default:
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if got := r.URL.Query().Get("client_id"); got != fresh {
				t.Errorf("retried with %q, want the fresh id", got)
			}
			_, _ = fmt.Fprint(w, trackJSON(""))
		}
	})
	// The bundle the home page points at has to be reachable, so it is the
	// test server's own.
	client.home = client.base + "/"
	client.assets = regexp.MustCompile(regexp.QuoteMeta(client.base) + `/player\.js`)

	track, err := client.Resolve(context.Background(), "https://soundcloud.com/x/y")
	if err != nil {
		t.Fatal(err)
	}
	if track.ID == 0 {
		t.Error("the retry did not produce a track")
	}
	if client.clientID != fresh {
		t.Errorf("client_id = %q, want the fresh one", client.clientID)
	}
}

func TestAnIDIsFoundHoweverItWasMinified(t *testing.T) {
	cases := []string{
		`{client_id:"abcdefghijklmnopqrstuvwxyz012345"}`,
		`client_id="abcdefghijklmnopqrstuvwxyz012345"`,
		`client_id=abcdefghijklmnopqrstuvwxyz012345&`,
		`,client_id : "abcdefghijklmnopqrstuvwxyz012345"`,
	}
	for _, source := range cases {
		match := idPattern.FindStringSubmatch(source)
		if match == nil {
			t.Errorf("no id found in %q", source)
			continue
		}
		if match[1] != testID {
			t.Errorf("id = %q from %q", match[1], source)
		}
	}
}

func TestAStoredIDIsReadBackAndDiscarded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storeID(testID)
	if got := readStoredID(); got != testID {
		t.Errorf("readStoredID() = %q, want the stored id", got)
	}

	client := &Client{http: http.DefaultClient, clientID: testID}
	client.forget(testID)
	if got := readStoredID(); got != "" {
		t.Errorf("a refused id survived on disk: %q", got)
	}
}

// Garbage in the cache file is not an id.
func TestAGarbledCacheIsIgnored(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storeID("too-short")
	if got := readStoredID(); got != "" {
		t.Errorf("readStoredID() = %q, want empty", got)
	}
}

func TestTheClientIDStaysOutOfErrors(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	_, err := client.Resolve(context.Background(), "https://soundcloud.com/x/y")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), testID) {
		t.Errorf("the error leaked the client_id: %v", err)
	}
}

// The API answering with something that is not JSON is a shape change, which
// is exactly when the yt-dlp fallback has to be able to take over.
func TestAnUnreadableAnswerIsAnError(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "<html>maintenance</html>")
	})
	if _, err := client.Resolve(context.Background(), "https://soundcloud.com/x/y"); err == nil {
		t.Error("want an error for a non-JSON answer")
	}
}

// A field the API stops sending must not take the rest of the track with it.
func TestAThinTrackStillResolves(t *testing.T) {
	client := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"kind":"track","id":5,"title":"x"}`)
	})
	track, err := client.Resolve(context.Background(), "https://soundcloud.com/x/y")
	if err != nil {
		t.Fatal(err)
	}
	if track.Title != "x" || track.Duration != 0 {
		t.Errorf("track = %+v", track)
	}
}
