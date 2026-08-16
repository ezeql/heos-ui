package tracklist

// Reading cue points out of free text.
//
// Every source ends up as text with timestamps in it — a SoundCloud
// description, a scraped page, a list pasted by hand — so there is one parser
// rather than one per provider.
//
// The hard part is not the timestamps, it is the noise around them.
// Descriptions are full of links, greetings and "recorded live at 2:00 AM",
// and a parser that takes those for cue points is worse than no parser at
// all: it would confidently name the wrong track. Hence the central rule — a
// timestamp only counts when it opens the line.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// cueLine matches a cue: an optional track number, an optionally bracketed
// timestamp, and whatever follows. Anchored at the start, which is what keeps
// prose out. The track number is only consumed when a timestamp follows it,
// so "12:34 A - B" does not lose its hour digits to it: ":" is not one of the
// separators that can close a number.
var cueLine = regexp.MustCompile(
	`^(?:\d{1,3}\s*[.)\]]\s*)?` + // track number: "01.", "1)", "3]"
		`[\[(]?\s*` + // timestamp may be bracketed
		`(?:(\d{1,2}):)?(\d{1,2}):(\d{2})` +
		`\s*[\])]?` +
		`\s*(?:[-–—:.|]\s*)?` + // separator before the text
		`(.+)$`,
)

// bullet strips list markers people put before the track number.
var bullet = regexp.MustCompile(`^[\s*•▶>·+]+`)

// separators between artist and title, in the order they are tried. Spaces
// are required on both sides: hyphens inside a title ("Re-Edit", "Anti-Hero")
// must survive.
var separators = []string{" - ", " – ", " — ", " | ", " -- "}

// A trailing bracket is a label only when it is not describing the track.
// Real labels and remix tags look identical structurally, so the distinction
// has to be made on the words themselves.
var notALabel = regexp.MustCompile(
	`(?i)\b(remix|mix|edit|vip|bootleg|dub|rework|refix|flip|dl|free|download|` +
		`unreleased|forthcoming|premiere|clip|version|extended|original)\b`,
)

var trailingBracket = regexp.MustCompile(`\s*\[([^\[\]]{1,60})\]\s*$`)

// durationSlack: cues past the end of the audio are a sign the text was not a
// tracklist at all, but a small overhang is normal — sets get uploaded with a
// few seconds trimmed.
const durationSlack = 60.0

var bareTimestamp = regexp.MustCompile(`^\s*(?:(\d{1,2}):)?(\d{1,2}):(\d{2})\s*$`)

// ParseTimestamp converts "h:mm:ss" or "mm:ss" to seconds. The second result
// is false when the text is neither.
func ParseTimestamp(text string) (float64, bool) {
	match := bareTimestamp.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	return timestampSeconds(match[1], match[2], match[3])
}

// timestampSeconds converts matched clock fields, rejecting impossible
// values.
func timestampSeconds(h, m, s string) (float64, bool) {
	hours := 0
	if h != "" {
		hours, _ = strconv.Atoi(h)
	}
	minutes, _ := strconv.Atoi(m)
	secs, _ := strconv.Atoi(s)
	if secs >= 60 {
		return 0, false
	}
	// Minutes only run past 59 when they are the largest unit: "75:20" is a
	// legitimate way to write an hour and a quarter, "1:75:20" is not.
	if minutes >= 60 && hours > 0 {
		return 0, false
	}
	return float64(hours*3600 + minutes*60 + secs), true
}

// splitArtistTitle splits on the first separator: titles hold more dashes
// than artists do.
func splitArtistTitle(text string) (artist, title string) {
	for _, separator := range separators {
		if artist, title, found := strings.Cut(text, separator); found {
			if strings.TrimSpace(artist) != "" && strings.TrimSpace(title) != "" {
				return strings.TrimSpace(artist), strings.TrimSpace(title)
			}
		}
	}
	return "", strings.TrimSpace(text)
}

// extractLabel peels off a trailing "[Label]", leaving remix tags where they
// are.
func extractLabel(text string) (body, label string) {
	loc := trailingBracket.FindStringSubmatchIndex(text)
	if loc == nil {
		return strings.TrimSpace(text), ""
	}
	inner := strings.TrimSpace(text[loc[2]:loc[3]])
	if inner == "" || notALabel.MatchString(inner) {
		return strings.TrimSpace(text), ""
	}
	return strings.TrimSpace(text[:loc[0]]), inner
}

func isUnknownMarker(value string) bool {
	if value == "" {
		return false
	}
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(value)), ".!")
	return unknownMarkers[normalized]
}

// cueFrom builds a cue from the text following a timestamp. The zero Cue and
// false mean the text held nothing usable.
func cueFrom(rest string, start float64) (Cue, bool) {
	body, label := extractLabel(strings.TrimSpace(rest))
	if body == "" {
		return Cue{}, false
	}
	artist, title := splitArtistTitle(body)

	if isUnknownMarker(title) && (artist == "" || isUnknownMarker(artist)) {
		// Collapse every spelling of an unidentified track into one, so the
		// display does not alternate between "ID", "id?" and "???".
		return Cue{Start: start, Title: "ID", Unknown: true, Label: label}, true
	}
	return Cue{Start: start, Title: title, Artist: artist, Label: label}, true
}

// tidy sorts, deduplicates and drops what cannot be right.
func tidy(cues []Cue, duration float64) []Cue {
	if len(cues) == 0 {
		return nil
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Start < cues[j].Start })

	// Two cues at the same second means the source listed a track twice, or a
	// comment repeated one. Keep the more descriptive of the two.
	collapsed := cues[:0]
	for _, cue := range cues {
		if len(collapsed) > 0 && collapsed[len(collapsed)-1].Start == cue.Start {
			if len(cue.Describe()) > len(collapsed[len(collapsed)-1].Describe()) {
				collapsed[len(collapsed)-1] = cue
			}
			continue
		}
		collapsed = append(collapsed, cue)
	}

	if duration > 0 {
		limit := duration + durationSlack
		kept := collapsed[:0]
		for _, cue := range collapsed {
			if cue.Start <= limit {
				kept = append(kept, cue)
			}
		}
		collapsed = kept
	}
	return collapsed
}

// ParseText extracts cue points from free text.
//
// It always returns a Tracklist, possibly an empty one: deciding whether a
// parse was good enough to use belongs to the provider chain, not here.
func ParseText(text, source, origin string, duration float64) Tracklist {
	var cues []Cue
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(bullet.ReplaceAllString(rawLine, ""))
		if line == "" {
			continue
		}
		match := cueLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		start, ok := timestampSeconds(match[1], match[2], match[3])
		if !ok {
			continue
		}
		if cue, ok := cueFrom(match[4], start); ok {
			cues = append(cues, cue)
		}
	}
	return Tracklist{
		Cues:     tidy(cues, duration),
		Source:   source,
		Origin:   origin,
		Duration: duration,
	}
}

// hasWordCharacter reports whether the text contains any letter or digit — an
// emoji-only reaction does not.
func hasWordCharacter(text string) bool {
	return strings.ContainsFunc(text, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	})
}
