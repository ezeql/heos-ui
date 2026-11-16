package tracklist

// Where tracklists come from, and in what order they are tried.
//
// Sources differ enormously in how much they can be trusted to be there. The
// SoundCloud description costs nothing and is right when it exists; the
// comments are free but noisy. So they are chained: the first one that yields
// something usable wins, and a source that is down is skipped rather than
// fatal.
//
// Nothing in here may fail loudly. "now" has to keep working when every
// provider fails.

import (
	"context"
	"log/slog"
	"time"
)

// MinCues is the fewest cues a parse must yield to be believed: below this a
// "tracklist" is more likely to be two stray timestamps in a description than
// an actual listing.
const MinCues = 3

// fetchTimeout: no provider gets to hold up the display.
const fetchTimeout = 20 * time.Second

// Provider is one source of tracklists.
type Provider interface {
	Name() string
	Supports(spec string) bool
	// Fetch returns nil when the source has nothing for this set; an error
	// means the source is broken, which the chain treats the same way.
	Fetch(ctx context.Context, spec string) (*Tracklist, error)
}

type options struct {
	cache     *Cache
	refresh   bool
	calibrate bool
	providers []Provider
	extract   extractor
	logger    *slog.Logger
}

func defaultsOptions() options {
	return options{
		calibrate: true,
		extract:   defaultExtract,
		logger:    slog.New(slog.DiscardHandler),
	}
}

// Option configures Find.
type Option func(*options)

// WithCache reads and writes the given cache.
func WithCache(cache *Cache) Option {
	return func(o *options) { o.cache = cache }
}

// WithRefresh bypasses the cache read (the result is still stored).
func WithRefresh() Option {
	return func(o *options) { o.refresh = true }
}

// WithoutCalibration skips measuring the audio offset in Find.
func WithoutCalibration() Option {
	return func(o *options) { o.calibrate = false }
}

// WithProviders replaces the default provider chain (the local file provider
// always stays first).
func WithProviders(providers ...Provider) Option {
	return func(o *options) { o.providers = providers }
}

// WithLogger sets the logger for skipped providers and other quiet failures.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// withExtractor swaps the yt-dlp invocation, for tests.
func withExtractor(extract extractor) Option {
	return func(o *options) { o.extract = extract }
}

// defaultProviders is the chain, cheapest and most trustworthy first.
func defaultProviders(extract extractor) []Provider {
	return []Provider{
		&DescriptionProvider{extract: extract},
		&CommentsProvider{extract: extract},
	}
}

func resolve(ctx context.Context, spec string, o *options) *Tracklist {
	if o.cache != nil && !o.refresh {
		if cached := o.cache.Get(spec); cached != nil {
			return cached
		}
	}

	// A path the user pointed at always wins: they typed it, they meant it.
	chain := []Provider{&LocalFileProvider{}}
	if o.providers != nil {
		chain = append(chain, o.providers...)
	} else {
		chain = append(chain, defaultProviders(o.extract)...)
	}

	for _, provider := range chain {
		if !provider.Supports(spec) {
			continue
		}
		found, err := func() (*Tracklist, error) {
			fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
			defer cancel()
			return provider.Fetch(fetchCtx, spec)
		}()
		if err != nil {
			// A broken source is a normal outcome here, not an error.
			o.logger.Debug("tracklist provider failed",
				"provider", provider.Name(), "error", err)
			continue
		}
		if found != nil && len(found.Cues) >= MinCues {
			if o.cache != nil {
				o.cache.Put(spec, *found)
			}
			return found
		}
	}
	return nil
}
