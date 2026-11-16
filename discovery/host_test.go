package discovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The config heoscli writes: the host is one key among others that belong to
// the Python tool.
const sampleConfig = "proxy_port = 8321\nhost = \"192.168.88.207\"\n"

func TestTheHostIsReadFromTheConfig(t *testing.T) {
	if got := configuredIn(sampleConfig); got != "192.168.88.207" {
		t.Errorf("host = %q, want 192.168.88.207", got)
	}
}

func TestAConfigWithoutAHostReadsEmpty(t *testing.T) {
	if got := configuredIn("proxy_port = 8321\n"); got != "" {
		t.Errorf("host = %q, want empty", got)
	}
}

// A host mentioned in passing is not the host key.
func TestOnlyTheHostKeyCounts(t *testing.T) {
	if got := configuredIn("# host = \"192.168.1.1\"\nproxy_host = \"1.2.3.4\"\n"); got != "" {
		t.Errorf("host = %q, want empty", got)
	}
}

func TestARewrittenHostLeavesTheRestAlone(t *testing.T) {
	updated := withHost(sampleConfig, "192.168.101.6")
	if got := configuredIn(updated); got != "192.168.101.6" {
		t.Errorf("host = %q, want 192.168.101.6", got)
	}
	if !strings.Contains(updated, "proxy_port = 8321") {
		t.Errorf("the rest of the config was lost:\n%s", updated)
	}
	if strings.Contains(updated, "192.168.88.207") {
		t.Errorf("the old host survived:\n%s", updated)
	}
}

func TestAConfigWithoutAHostGetsOne(t *testing.T) {
	updated := withHost("proxy_port = 8321", "192.168.101.6")
	if got := configuredIn(updated); got != "192.168.101.6" {
		t.Errorf("host = %q, want 192.168.101.6", got)
	}
	if !strings.Contains(updated, "proxy_port = 8321") {
		t.Errorf("the rest of the config was lost:\n%s", updated)
	}
}

func TestAnEmptyConfigGetsAHost(t *testing.T) {
	if got := configuredIn(withHost("", "192.168.101.6")); got != "192.168.101.6" {
		t.Errorf("host = %q, want 192.168.101.6", got)
	}
}

func TestARememberedHostComesBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".config", "heoscli", "config.toml")
	if err := os.MkdirAll(filepath.Dir(config), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Remember("192.168.101.6"); err != nil {
		t.Fatal(err)
	}
	if got := Configured(); got != "192.168.101.6" {
		t.Errorf("Configured() = %q, want 192.168.101.6", got)
	}
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "proxy_port = 8321") {
		t.Errorf("Remember lost the rest of the config:\n%s", raw)
	}
}

// Remembering into a home with no config at all is the first-run case.
func TestRememberingWithoutAConfigWritesOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Remember("192.168.101.6"); err != nil {
		t.Fatal(err)
	}
	if got := Configured(); got != "192.168.101.6" {
		t.Errorf("Configured() = %q, want 192.168.101.6", got)
	}
}

func TestNoTemporaryFileIsLeftBehind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := Remember("192.168.101.6"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(configPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("left behind %s", entry.Name())
		}
	}
}

// An explicit host is taken at face value: no config is read and no search
// runs, which is the whole point of passing one.
func TestAnOverrideSkipsTheSearch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	host, err := Resolve(context.Background(), "10.0.0.9", nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "10.0.0.9" {
		t.Errorf("host = %q, want 10.0.0.9", host)
	}
}
