package soundcloud

// Getting in.
//
// The private API wants a client_id, and SoundCloud hands one to every
// visitor: it is compiled into the JavaScript the web player itself loads.
// Reading it back out is what yt-dlp does too, and it is the one piece here
// that can break without warning, because the id rotates every few weeks.
//
// So it is cached on disk next to heoscli's other state, and thrown away the
// moment the API refuses it. The cost of being wrong is one extra round trip.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"

	"github.com/ezeql/heos-ui/internal/config"
)

const homePage = "https://soundcloud.com/"

var (
	// assetPattern finds the player's JavaScript bundles in the home page.
	assetPattern = regexp.MustCompile(`https://[a-z0-9\-]+\.sndcdn\.com/assets/[^"'\s]+\.js`)

	// idPattern finds the id inside one of them, however it was minified:
	// client_id:"…", client_id="…", client_id=…
	idPattern = regexp.MustCompile(`client_id["'\s:=]{1,4}([A-Za-z0-9]{32})`)

	// storedPattern validates what came back from the cache file, which holds
	// the bare id and nothing else.
	storedPattern = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
)

// scrapeWidth is how many bundles are read at once. The id sits in one of
// them and there is no telling which, so a few are tried in parallel; more
// than a few would pull megabytes of JavaScript to save a second.
const scrapeWidth = 4

// ClientID returns the credential every call needs, reading it from memory,
// then from disk, and scraping it only when neither has one.
func (c *Client) ClientID(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.clientID != "" {
		defer c.mu.Unlock()
		return c.clientID, nil
	}
	c.mu.Unlock()

	if stored := readStoredID(); stored != "" {
		c.mu.Lock()
		c.clientID = stored
		c.mu.Unlock()
		return stored, nil
	}

	found, err := c.scrapeClientID(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.clientID = found
	c.mu.Unlock()
	storeID(found)
	return found, nil
}

// forget discards an id the API has refused, so the next call scrapes a fresh
// one. The id is passed in because another goroutine may already have
// replaced it, and throwing away a working id would just cost another scrape.
func (c *Client) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clientID != id {
		return
	}
	c.clientID = ""
	_ = os.Remove(idPath())
}

// scrapeClientID reads the id out of the player's own JavaScript.
func (c *Client) scrapeClientID(ctx context.Context) (string, error) {
	page, err := c.plainGet(ctx, c.home)
	if err != nil {
		return "", fmt.Errorf("soundcloud: could not read the home page: %w", err)
	}
	pattern := c.assets
	if pattern == nil {
		pattern = assetPattern
	}
	assets := pattern.FindAllString(string(page), -1)
	if len(assets) == 0 {
		return "", errors.New("soundcloud: the home page listed no player bundles")
	}

	// The id lives in one of the last bundles far more often than the first,
	// so they are tried from the end.
	for start := len(assets) - 1; start >= 0; start -= scrapeWidth {
		end := max(start-scrapeWidth+1, 0)
		if id := c.scanBundles(ctx, assets[end:start+1]); id != "" {
			return id, nil
		}
	}
	return "", errors.New("soundcloud: no client_id in any player bundle")
}

// scanBundles reads a handful of bundles at once and returns the first id.
func (c *Client) scanBundles(ctx context.Context, assets []string) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu    sync.Mutex
		found string
		wg    sync.WaitGroup
	)
	for _, asset := range assets {
		wg.Go(func() {
			body, err := c.plainGet(ctx, asset)
			if err != nil {
				return
			}
			match := idPattern.FindSubmatch(body)
			if match == nil {
				return
			}
			mu.Lock()
			if found == "" {
				found = string(match[1])
				cancel() // the others are reading megabytes for nothing now
			}
			mu.Unlock()
		})
	}
	wg.Wait()
	return found
}

// plainGet fetches a URL that needs no credentials.
func (c *Client) plainGet(ctx context.Context, target string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 16<<20))
}

// idPath is where a working id is kept between runs, next to heoscli's cache.
// It is not a secret — every visitor is handed the same one — so it is stored
// in the clear, and losing it costs one scrape.
func idPath() string {
	return config.Path("soundcloud_client_id")
}

func readStoredID() string {
	raw, err := os.ReadFile(idPath())
	if err != nil {
		return ""
	}
	id := string(bytes.TrimSpace(raw))
	if !storedPattern.MatchString(id) {
		return ""
	}
	return id
}

func storeID(id string) {
	_ = config.WriteAtomic(idPath(), []byte(id+"\n"))
}
