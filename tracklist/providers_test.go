package tracklist

// The provider chain. Nothing in it may fail loudly: the display has to keep
// working when every source is down.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvider is a scripted chain link.
type fakeProvider struct {
	name     string
	supports bool
	result   *Tracklist
	err      error
	calls    int
}

func (p *fakeProvider) Name() string         { return p.name }
func (p *fakeProvider) Supports(string) bool { return p.supports }
func (p *fakeProvider) Fetch(context.Context, string) (*Tracklist, error) {
	p.calls++
	return p.result, p.err
}

func usable(source string) *Tracklist {
	return &Tracklist{
		Cues: []Cue{
			{Start: 0, Title: "One"}, {Start: 300, Title: "Two"}, {Start: 600, Title: "Three"},
		},
		Source: source,
	}
}

func TestTheFirstUsableSourceWins(t *testing.T) {
	t.Parallel()
	second := &fakeProvider{name: "second", supports: true, result: usable("second")}
	found, _ := Find(context.Background(), "https://example.com/set",
		WithProviders(
			&fakeProvider{name: "first", supports: true, result: usable("first")},
			second,
		))
	if found == nil || found.Source != "first" {
		t.Errorf("found = %+v", found)
	}
	if second.calls != 0 {
		t.Error("the chain must stop at the first usable source")
	}
}

func TestAProviderThatFailsIsSkipped(t *testing.T) {
	t.Parallel()
	found, _ := Find(context.Background(), "https://example.com/set",
		WithProviders(
			&fakeProvider{name: "broken", supports: true, err: errors.New("boom")},
			&fakeProvider{name: "working", supports: true, result: usable("working")},
		))
	if found == nil || found.Source != "working" {
		t.Errorf("found = %+v", found)
	}
}

func TestAThinResultIsNotGoodEnough(t *testing.T) {
	t.Parallel()
	thin := &Tracklist{Cues: []Cue{{Start: 0, Title: "Only"}}, Source: "thin"}
	found, _ := Find(context.Background(), "https://example.com/set",
		WithProviders(
			&fakeProvider{name: "thin", supports: true, result: thin},
			&fakeProvider{name: "full", supports: true, result: usable("full")},
		))
	if found == nil || found.Source != "full" {
		t.Errorf("found = %+v", found)
	}
}

func TestAProviderThatDoesNotHandleThisLinkIsNotCalled(t *testing.T) {
	t.Parallel()
	other := &fakeProvider{name: "other", supports: false, result: usable("other")}
	Find(context.Background(), "https://example.com/set", WithProviders(other))
	if other.calls != 0 {
		t.Error("unsupported provider was called")
	}
}

func TestNoSourceHasIt(t *testing.T) {
	t.Parallel()
	if found, _ := Find(context.Background(), "https://example.com/set",
		WithProviders(&fakeProvider{name: "empty", supports: true})); found != nil {
		t.Errorf("found = %+v", found)
	}
}

func TestEverythingFailingIsStillJustNil(t *testing.T) {
	t.Parallel()
	found, _ := Find(context.Background(), "https://example.com/set",
		WithProviders(
			&fakeProvider{name: "a", supports: true, err: errors.New("down")},
			&fakeProvider{name: "b", supports: true, err: errors.New("also down")},
		))
	if found != nil {
		t.Errorf("found = %+v", found)
	}
}

func TestACachedTracklistSkipsTheChain(t *testing.T) {
	t.Parallel()
	cache := NewCache(filepath.Join(t.TempDir(), "tracklists.json"))
	cache.Put("https://example.com/set", *usable("cached"))
	provider := &fakeProvider{name: "network", supports: true, result: usable("network")}

	found, _ := Find(context.Background(), "https://example.com/set",
		WithCache(cache), WithProviders(provider))
	if found == nil || found.Source != "cached" {
		t.Errorf("found = %+v", found)
	}
	if provider.calls != 0 {
		t.Error("cache hit must not touch the chain")
	}
}

func TestRefreshIgnoresTheCache(t *testing.T) {
	t.Parallel()
	cache := NewCache(filepath.Join(t.TempDir(), "tracklists.json"))
	cache.Put("https://example.com/set", *usable("cached"))
	provider := &fakeProvider{name: "network", supports: true, result: usable("network")}

	found, _ := Find(context.Background(), "https://example.com/set",
		WithCache(cache), WithRefresh(), WithProviders(provider))
	if found == nil || found.Source != "network" {
		t.Errorf("found = %+v", found)
	}
	// And the fresh result replaces the cached one.
	if got := cache.Get("https://example.com/set"); got == nil || got.Source != "network" {
		t.Errorf("cache = %+v", got)
	}
}

// -- soundcloud ------------------------------------------------------------

const description = `Tracklist:
0:00 First Artist - Opening
4:35 Second Artist - Middle
9:10 Third Artist - Closer`

func fixedExtractor(info *mediaInfo, err error) (extractor, *int) {
	calls := 0
	return func(context.Context, string, bool) (*mediaInfo, error) {
		calls++
		return info, err
	}, &calls
}

func TestTheDescriptionIsFetchedWhenNotAlreadyKnown(t *testing.T) {
	t.Parallel()
	extract, _ := fixedExtractor(&mediaInfo{Description: description, Duration: 3600}, nil)
	provider := &DescriptionProvider{extract: extract}
	found, err := provider.Fetch(context.Background(), "https://soundcloud.com/dj/set")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Len() != 3 || found.Source != "soundcloud-description" {
		t.Errorf("found = %+v", found)
	}
}

func TestASetWithNoDescription(t *testing.T) {
	t.Parallel()
	extract, _ := fixedExtractor(&mediaInfo{Duration: 3600}, nil)
	provider := &DescriptionProvider{extract: extract}
	found, err := provider.Fetch(context.Background(), "https://soundcloud.com/dj/set")
	if err != nil || found != nil {
		t.Errorf("found = %+v, err = %v", found, err)
	}
}

func TestTheSoundcloudProvidersOnlyTakeSoundcloudLinks(t *testing.T) {
	t.Parallel()
	providers := []Provider{&DescriptionProvider{}, &CommentsProvider{}}
	for _, provider := range providers {
		for spec, want := range map[string]bool{
			"https://soundcloud.com/dj/set":    true,
			"https://on.soundcloud.com/xyz":    true,
			"https://www.soundcloud.com/dj/s":  true,
			"https://mixcloud.com/dj/set":      false,
			"https://notsoundcloud.com/dj/set": false,
			"/home/user/set.txt":               false,
		} {
			if got := provider.Supports(spec); got != want {
				t.Errorf("%s.Supports(%q) = %v, want %v", provider.Name(), spec, got, want)
			}
		}
	}
}

func timed(moment float64, text string) mediaComment {
	return mediaComment{StartTime: &moment, Text: text}
}

func TestCommentsNamingATrackBecomeCues(t *testing.T) {
	t.Parallel()
	cues := cuesFromComments([]mediaComment{
		timed(120, "Some Artist - Some Track"),
		timed(600, "Another Artist - Another Track"),
	}, 3600)
	if len(cues) != 2 || cues[0].Artist != "Some Artist" || cues[1].Start != 600 {
		t.Errorf("cues = %+v", cues)
	}
}

func TestApplauseIsNotACue(t *testing.T) {
	t.Parallel()
	cues := cuesFromComments([]mediaComment{
		timed(120, "🔥🔥🔥"),
		timed(240, "yes"),
		timed(300, "this is such a great set honestly"),
		timed(400, "tune"),
	}, 3600)
	if len(cues) != 0 {
		t.Errorf("cues = %+v", cues)
	}
}

func TestTheOffsetComesFromSoundcloudNotFromTheText(t *testing.T) {
	t.Parallel()
	// The comment says 10:00; SoundCloud pinned it at 120 seconds.
	cues := cuesFromComments([]mediaComment{
		timed(120, "10:00 Some Artist - Some Track"),
	}, 3600)
	if len(cues) != 1 || cues[0].Start != 120 {
		t.Errorf("cues = %+v", cues)
	}
}

func TestTwoPeopleNamingTheSameDropMakeOneCue(t *testing.T) {
	t.Parallel()
	cues := cuesFromComments([]mediaComment{
		timed(120, "Artist - Track"),
		timed(130, "Some Artist - Some Track Extended Edition"),
	}, 3600)
	if len(cues) != 1 {
		t.Fatalf("cues = %+v", cues)
	}
	// Same moment, two people: prefer the one that said more.
	if cues[0].Artist != "Some Artist" {
		t.Errorf("cue = %+v", cues[0])
	}
}

func TestAnIDRequestIsACueWorthKeeping(t *testing.T) {
	t.Parallel()
	cues := cuesFromComments([]mediaComment{timed(120, "ID - ID")}, 3600)
	if len(cues) != 1 || !cues[0].Unknown {
		t.Errorf("cues = %+v", cues)
	}
}

func TestCommentsPastTheEndAreDropped(t *testing.T) {
	t.Parallel()
	cues := cuesFromComments([]mediaComment{timed(4000, "Artist - Track")}, 3600)
	if len(cues) != 0 {
		t.Errorf("cues = %+v", cues)
	}
}

func TestCommentsWithoutAPositionAreIgnored(t *testing.T) {
	t.Parallel()
	cues := cuesFromComments([]mediaComment{{Text: "Artist - Track"}}, 3600)
	if len(cues) != 0 {
		t.Errorf("cues = %+v", cues)
	}
}

func TestTheDefaultChainIsOrderedCheapestFirst(t *testing.T) {
	t.Parallel()
	chain := defaultProviders(nil)
	names := make([]string, len(chain))
	for i, provider := range chain {
		names[i] = provider.Name()
	}
	want := "soundcloud-description,soundcloud-comments"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("chain = %s, want %s", got, want)
	}
}

// -- find ------------------------------------------------------------------

func TestFindReturnsAStoredOffsetWithoutCalibrating(t *testing.T) {
	t.Parallel()
	cache := NewCache(filepath.Join(t.TempDir(), "tracklists.json"))
	cache.Put(spec, *usable("1001tracklists"))
	cache.SetOffset(spec, 42)

	extract, calls := fixedExtractor(nil, errors.New("must not be called"))
	found, offset := Find(context.Background(), spec,
		WithCache(cache), withExtractor(extract))
	if found == nil || offset != 42 {
		t.Errorf("found = %+v, offset = %v", found, offset)
	}
	if *calls != 0 {
		t.Error("a stored offset outranks anything measured now")
	}
}

func TestFindDoesNotCalibrateSourcesAlignedToTheAudio(t *testing.T) {
	t.Parallel()
	// A tracklist from the comments was measured against this upload;
	// aligning it against those same comments would be circular.
	extract, calls := fixedExtractor(nil, errors.New("must not be called"))
	found, offset := Find(context.Background(), spec,
		WithProviders(&fakeProvider{name: "sc", supports: true, result: usable("soundcloud-comments")}),
		withExtractor(extract))
	if found == nil || offset != 0 {
		t.Errorf("found = %+v, offset = %v", found, offset)
	}
	if *calls != 0 {
		t.Error("an aligned source must not be calibrated")
	}
}

func TestFindCalibratesADatabaseTracklist(t *testing.T) {
	t.Parallel()
	// A database tracklist whose cues run 45 seconds early, and comments
	// reacting where the audio actually is.
	shifted := listing(cueStarts, alignDuration, "1001tracklists")
	comments := make([]mediaComment, 0)
	for _, moment := range commentsReactingTo(cueStarts, 45) {
		comments = append(comments, timed(moment, fmt.Sprintf("reaction at %f", moment)))
	}
	extract, _ := fixedExtractor(&mediaInfo{Duration: alignDuration, Comments: comments}, nil)

	found, offset := Find(context.Background(), spec,
		WithProviders(&fakeProvider{name: "db", supports: true, result: &shifted}),
		withExtractor(extract))
	if found == nil {
		t.Fatal("no tracklist")
	}
	if offset < 40 || offset > 50 {
		t.Errorf("offset = %v, want ≈45", offset)
	}
}

func TestFindStoresTheMeasuredOffset(t *testing.T) {
	t.Parallel()
	cache := NewCache(filepath.Join(t.TempDir(), "tracklists.json"))
	shifted := listing(cueStarts, alignDuration, "1001tracklists")
	comments := make([]mediaComment, 0)
	for _, moment := range commentsReactingTo(cueStarts, 45) {
		comments = append(comments, timed(moment, "reaction"))
	}
	extract, _ := fixedExtractor(&mediaInfo{Duration: alignDuration, Comments: comments}, nil)

	_, offset := Find(context.Background(), spec,
		WithCache(cache),
		WithProviders(&fakeProvider{name: "db", supports: true, result: &shifted}),
		withExtractor(extract))
	if offset == 0 {
		t.Fatal("no offset measured")
	}
	if stored := cache.OffsetFor(spec); stored != offset {
		t.Errorf("stored = %v, measured = %v", stored, offset)
	}
}

func TestFindWithNothingAnywhere(t *testing.T) {
	t.Parallel()
	found, offset := Find(context.Background(), "https://example.com/set",
		WithProviders(&fakeProvider{name: "empty", supports: true}))
	if found != nil || offset != 0 {
		t.Errorf("found = %+v, offset = %v", found, offset)
	}
}

// -- local file ------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestALocalFileAlwaysWins(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "set.txt")
	writeFile(t, path, "0:00 A - One\n5:00 B - Two\n10:00 C - Three")
	network := &fakeProvider{name: "network", supports: true, result: usable("network")}

	found, _ := Find(context.Background(), path, WithProviders(network))
	if found == nil || found.Source != "local" {
		t.Errorf("found = %+v", found)
	}
	if network.calls != 0 {
		t.Error("a path the user pointed at must win without touching the network")
	}
}
