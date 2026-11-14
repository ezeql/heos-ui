// Command ui serves the web UI: the page in webui/static and the JSON API it
// polls, talking to the speakers through goheos.
//
// Usage:
//
//	ui [-host IP] [-port 8080]
//
// The HEOS host defaults to the one cached in
// ~/.config/heoscli/config.toml, and when that one does not answer the
// speaker is discovered on the local network instead.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/ezeql/goheos"

	"github.com/ezeql/heoscli-go/discovery"
	"github.com/ezeql/heoscli-go/webui"
)

func main() {
	hostFlag := flag.String("host", "", "HEOS device (default: the cached host, else discovered)")
	port := flag.Int("port", 8080, "port to serve the UI on")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	host, err := discovery.Resolve(ctx, *hostFlag, log.Printf)
	if err != nil {
		log.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	heos, err := goheos.Connect(ctx, host, goheos.WithAutoReconnect(time.Second, 0))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = heos.Disconnect() }()

	server := webui.New(heos, nil, logger)
	fmt.Printf("heoscli ui (go) — http://localhost:%d — speaker %s\n", *port, host)
	if err := server.Serve(ctx, fmt.Sprintf(":%d", *port)); err != nil {
		log.Fatal(err)
	}
}
