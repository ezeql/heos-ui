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
//
// This machine is served as is. Any other device needs the link printed at
// startup, opened once: it carries the key in ~/.config/heoscli/heos-ui.key.
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

	"github.com/ezeql/heos-ui/discovery"
	"github.com/ezeql/heos-ui/webui"
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
	fmt.Printf("heos-ui — http://localhost:%d — speaker %s\n", *port, host)
	// Other devices get in with a key, once, through this link.
	if key, err := webui.LoadKey(); err != nil {
		log.Printf("no key for other devices, so only this machine is served: %v", err)
	} else {
		server.SetKey(key)
		if ip, err := webui.LocalIP(host); err == nil {
			fmt.Printf("on a phone or another computer: http://%s:%d/?key=%s\n", ip, *port, key)
		}
	}
	if err := server.Serve(ctx, fmt.Sprintf(":%d", *port)); err != nil {
		log.Fatal(err)
	}
}
