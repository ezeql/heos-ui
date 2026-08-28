package discovery

// Resolving a host is its own small policy: a flag beats the remembered
// speaker, the remembered speaker beats a search, and whatever a search finds
// gets remembered so the next run is instant again. Every tool here wants the
// same policy, which is why it lives next to the search rather than in each
// command.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// searchTimeout is how long a search waits for SSDP answers. The M-SEARCH
// asks devices to reply within two seconds, so waiting much longer only
// delays a network where nothing is going to answer at all.
const searchTimeout = 2 * time.Second

// reachTimeout is how long the remembered host gets to prove it is still
// there before a search is preferred over it.
const reachTimeout = 1500 * time.Millisecond

// hostPattern reads the host line of heoscli's config, which is TOML written
// by the Python tool; only this one key is ever touched here.
var hostPattern = regexp.MustCompile(`(?m)^\s*host\s*=\s*"([^"]*)"\s*$`)

// Logf is where progress goes while a search runs, since it is the one thing
// here slow enough for a user to wonder about. A nil Logf is silent.
type Logf func(format string, args ...any)

func (l Logf) printf(format string, args ...any) {
	if l != nil {
		l(format, args...)
	}
}

// ConfigPath is heoscli's config file, shared with the Python implementation.
// Python heoscli hardcodes ~/.config/heoscli on every platform, and sharing
// the file matters more than the platform convention does.
func ConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "config.toml")
	}
	return filepath.Join(home, ".config", "heoscli", "config.toml")
}

// Configured returns the speaker heoscli already knows about, or an empty
// string when it knows about none.
func Configured() string {
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return ""
	}
	return configuredIn(string(raw))
}

// configuredIn reads the host out of a config file's text.
func configuredIn(config string) string {
	if found := hostPattern.FindStringSubmatch(config); found != nil {
		return strings.TrimSpace(found[1])
	}
	return ""
}

// Remember writes host back to the config, so the next run skips the search.
// The rest of the file belongs to the Python tool — proxy_port and whatever
// else it keeps there — so only the host line is rewritten.
func Remember(host string) error {
	path := ConfigPath()
	//nolint:gosec // the path is this tool's own config file, never user input
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	updated := withHost(string(existing), host)

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary := path + ".tmp"
	//nolint:gosec // same file, written back where it was read from
	if err := os.WriteFile(temporary, []byte(updated), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// withHost replaces the host line in a config, or appends one when the file
// has none. Everything else survives byte for byte.
func withHost(config, host string) string {
	line := fmt.Sprintf("host = %q", host)
	if hostPattern.MatchString(config) {
		return hostPattern.ReplaceAllLiteralString(config, line)
	}
	if config != "" && !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	return config + line + "\n"
}

// Resolve answers the question every command here opens with: which speaker.
//
// An explicit host is taken at face value, because overriding is the whole
// point of passing one. Otherwise the remembered speaker is used if it still
// answers — a speaker that moved to another address, or a laptop that moved
// to another network, is exactly the case that used to end in a timeout — and
// a search fills in when it does not.
func Resolve(ctx context.Context, override string, log Logf) (string, error) {
	if override != "" {
		return override, nil
	}

	if remembered := Configured(); remembered != "" {
		if Reachable(ctx, remembered, reachTimeout) {
			return remembered, nil
		}
		log.printf("speaker %s did not answer, looking for another one", remembered)
	}

	log.printf("searching for HEOS speakers...")
	found := Search(ctx, searchTimeout)
	if len(found) == 0 {
		return "", errors.New("no HEOS speaker found on this network: pass -host to name one")
	}
	chosen := found[0]
	if len(found) > 1 {
		var others []string
		for _, device := range found[1:] {
			others = append(others, device.String())
		}
		log.printf("found %d speakers, using %s (others: %s; pass -host to pick)",
			len(found), chosen, strings.Join(others, ", "))
	} else {
		log.printf("found %s", chosen)
	}

	// A speaker that was found is worth remembering, but failing to write the
	// config is not worth failing the run over.
	if err := Remember(chosen.Host); err != nil {
		log.printf("could not save the host to %s: %v", ConfigPath(), err)
	}
	return chosen.Host, nil
}
