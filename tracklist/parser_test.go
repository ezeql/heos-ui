package tracklist

// Reading cue points out of the text people actually write.
//
// Descriptions are not data. They are prose with a tracklist somewhere
// inside, surrounded by links, greetings and the occasional time of day, so
// most of these cases are about what the parser must refuse to believe.

import (
	"strings"
	"testing"
)

func parsedCues(t *testing.T, text string, duration float64) []Cue {
	t.Helper()
	return ParseText(text, "test", "", duration).Cues
}

func TestParsesClockValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text string
		want float64
	}{
		{"0:00", 0},
		{"5:20", 320},
		{"12:33", 753},
		// The same digits mean something else with an hour in front.
		{"1:02:33", 3753},
		{"1:12:33", 4353},
		// Minutes past 59 are fine as the largest unit: a 75-minute set.
		{"75:20", 4520},
		{"00:00:01", 1},
	}
	for _, tt := range tests {
		got, ok := ParseTimestamp(tt.text)
		if !ok || got != tt.want {
			t.Errorf("ParseTimestamp(%q) = %v/%v, want %v", tt.text, got, ok, tt.want)
		}
	}
}

func TestFormatTimestampRoundTrips(t *testing.T) {
	t.Parallel()
	for _, seconds := range []float64{0, 59, 320, 3599, 3600, 3753, 36000 + 61} {
		formatted := FormatTimestamp(seconds)
		if got, ok := ParseTimestamp(formatted); !ok || got != seconds {
			t.Errorf("ParseTimestamp(FormatTimestamp(%v) = %q) = %v/%v", seconds, formatted, got, ok)
		}
	}
}

func TestRejectsImpossibleClockValues(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"2:5", "99:99", "1:75:20", "", "abc", "12", "5-20"} {
		if _, ok := ParseTimestamp(text); ok {
			t.Errorf("ParseTimestamp(%q) accepted an impossible value", text)
		}
	}
}

func TestAcceptsTheShapesPeopleWrite(t *testing.T) {
	t.Parallel()
	lines := []string{
		"0:00 Artist - Title",
		"[0:00] Artist - Title",
		"(0:00) Artist - Title",
		"0:00 - Artist - Title",
		"01. 0:00 Artist - Title",
		"1) 0:00 Artist - Title",
		"• 0:00 Artist - Title",
		"0:00 Artist – Title",
		"0:00 Artist — Title",
		"0:00 Artist | Title",
	}
	for _, line := range lines {
		cues := parsedCues(t, line, 0)
		if len(cues) != 1 {
			t.Errorf("%q: parsed %d cues, want 1", line, len(cues))
			continue
		}
		if cues[0].Start != 0 || cues[0].Artist != "Artist" || cues[0].Title != "Title" {
			t.Errorf("%q: cue = %+v", line, cues[0])
		}
	}
}

func TestIgnoresEverythingThatIsNotACue(t *testing.T) {
	t.Parallel()
	lines := []string{
		// The rule that keeps prose out: a timestamp mid-sentence is not a cue.
		"Recorded live at 2:00 in the club",
		"Follow me on instagram",
		"https://example.com/some/link",
		"Tracklist:",
		"",
		"   ",
		// A track number with no timestamp after it.
		"01. Artist - Title",
		// Impossible clock values never become cues either.
		"99:99 Artist - Title",
	}
	for _, line := range lines {
		if cues := parsedCues(t, line, 0); len(cues) != 0 {
			t.Errorf("%q: parsed %d cues, want 0: %+v", line, len(cues), cues)
		}
	}
}

func TestReadsATracklistOutOfADescription(t *testing.T) {
	t.Parallel()
	cues := parsedCues(t, strings.Join([]string{
		"Recorded at 3:00 AM, mastered by someone",
		"",
		"Tracklist:",
		"1. 0:00 First Artist - Opening",
		"2. 4:35 Second Artist - Middle Track",
		"3. 1:02:10 Third Artist - Closer",
		"",
		"Follow me: example.com",
	}, "\n"), 0)
	if len(cues) != 3 {
		t.Fatalf("parsed %d cues, want 3: %+v", len(cues), cues)
	}
	wantStarts := []float64{0, 275, 3730}
	wantArtists := []string{"First Artist", "Second Artist", "Third Artist"}
	for i, cue := range cues {
		if cue.Start != wantStarts[i] || cue.Artist != wantArtists[i] {
			t.Errorf("cue %d = %+v", i, cue)
		}
	}
}

func TestSplitsOnTheFirstSeparator(t *testing.T) {
	t.Parallel()
	// Titles hold more dashes than artist names do.
	cues := parsedCues(t, "5:20 Some Artist - Some Track - Special Version", 0)
	if cues[0].Artist != "Some Artist" || cues[0].Title != "Some Track - Special Version" {
		t.Errorf("cue = %+v", cues[0])
	}
}

func TestKeepsHyphensInsideWords(t *testing.T) {
	t.Parallel()
	cues := parsedCues(t, "5:20 Anti-Hero - Re-Edit Of Something", 0)
	if cues[0].Artist != "Anti-Hero" || cues[0].Title != "Re-Edit Of Something" {
		t.Errorf("cue = %+v", cues[0])
	}
}

func TestALineWithNoSeparatorIsAllTitle(t *testing.T) {
	t.Parallel()
	cues := parsedCues(t, "5:20 Something Nobody Split Properly", 0)
	if cues[0].Artist != "" || cues[0].Title != "Something Nobody Split Properly" {
		t.Errorf("cue = %+v", cues[0])
	}
}

func TestTrailingBracketIsReadAsALabel(t *testing.T) {
	t.Parallel()
	cues := parsedCues(t, "5:20 Artist - Title [Some Records]", 0)
	if cues[0].Label != "Some Records" || cues[0].Title != "Title" {
		t.Errorf("cue = %+v", cues[0])
	}
}

func TestRemixTagsAreNotLabels(t *testing.T) {
	t.Parallel()
	// They are part of the title, and dropping them names the wrong track.
	for _, bracket := range []string{"[Remix]", "[VIP Mix]", "[FREE DOWNLOAD]", "[Bootleg]", "[Extended Mix]"} {
		cues := parsedCues(t, "5:20 Artist - Title "+bracket, 0)
		if cues[0].Label != "" || !strings.Contains(cues[0].Title, bracket) {
			t.Errorf("%s: cue = %+v", bracket, cues[0])
		}
	}
}

func TestUnidentifiedTracksCollapseToOneSpelling(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"ID - ID", "id - id", "ID? - ID?", "??? - ???"} {
		cues := parsedCues(t, "5:20 "+text, 0)
		if !cues[0].Unknown || cues[0].Title != "ID" {
			t.Errorf("%q: cue = %+v", text, cues[0])
		}
	}
}

func TestANamedTrackIsNotUnknown(t *testing.T) {
	t.Parallel()
	cues := parsedCues(t, "5:20 Artist - ID", 0)
	if cues[0].Unknown {
		t.Errorf("cue = %+v", cues[0])
	}
}

func TestOutOfOrderCuesAreSorted(t *testing.T) {
	t.Parallel()
	cues := parsedCues(t, "10:00 B - Two\n0:00 A - One\n5:00 C - Three", 0)
	if len(cues) != 3 || cues[0].Start != 0 || cues[1].Start != 300 || cues[2].Start != 600 {
		t.Errorf("cues = %+v", cues)
	}
}

func TestDuplicateTimestampsKeepTheMoreDescriptiveOne(t *testing.T) {
	t.Parallel()
	cues := parsedCues(t, "5:20 Artist - Title\n5:20 Artist - Title [Some Records]", 0)
	if len(cues) != 1 || cues[0].Label != "Some Records" {
		t.Errorf("cues = %+v", cues)
	}
}

func TestCuesPastTheEndOfTheAudioAreDropped(t *testing.T) {
	t.Parallel()
	// A tracklist that runs past the recording was written for another one.
	cues := parsedCues(t, "0:00 A - One\n5:00 B - Two\n2:00:00 C - Three", 600)
	if len(cues) != 2 || cues[0].Start != 0 || cues[1].Start != 300 {
		t.Errorf("cues = %+v", cues)
	}
}

func TestALittleOverhangIsTolerated(t *testing.T) {
	t.Parallel()
	if cues := parsedCues(t, "0:00 A - One\n10:30 B - Two", 600); len(cues) != 2 {
		t.Errorf("cues = %+v", cues)
	}
}

func TestTextWithNoTimestampsGivesAnEmptyTracklist(t *testing.T) {
	t.Parallel()
	parsed := ParseText("just some words\nand some more", "test", "", 0)
	if parsed.Len() != 0 {
		t.Errorf("cues = %+v", parsed.Cues)
	}
}
