// Command ui runs the heoscli web UI with the Go backend: the exact same
// page, served over goheos instead of pyheos.
//
// Usage:
//
//	ui [-host IP] [-port 8080]
//
// The HEOS host defaults to the one cached by heoscli in
// ~/.config/heoscli/config.toml.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ezeql/goheos"

	"github.com/ezeql/heoscli-go/webui"
)

var configHostPattern = regexp.MustCompile(`(?m)^host\s*=\s*"([^"]+)"`)

func configuredHost() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config", "heoscli", "config.toml"))
	if err != nil {
		return ""
	}
	if found := configHostPattern.FindSubmatch(raw); found != nil {
		return string(found[1])
	}
	return ""
}

func main() {
	host := flag.String("host", "", "HEOS device (default: the host cached by heoscli)")
	port := flag.Int("port", 8080, "port to serve the UI on")
	flag.Parse()
	if *host == "" {
		*host = configuredHost()
	}
	if *host == "" {
		log.Fatal("no host: pass -host or run heoscli once so it caches one")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	heos, err := goheos.Connect(ctx, *host, goheos.WithAutoReconnect(time.Second, 0))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = heos.Disconnect() }()

	server := webui.New(heos, nil, logger)
	fmt.Printf("heoscli ui (go) — http://localhost:%d — speaker %s\n", *port, *host)
	if err := server.Serve(ctx, fmt.Sprintf(":%d", *port)); err != nil {
		log.Fatal(err)
	}
}
