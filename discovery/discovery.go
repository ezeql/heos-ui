// Package discovery finds HEOS speakers on the local network, so that neither
// the user nor the tools that talk to them have to know an IP address.
//
// A HEOS device announces itself over SSDP under Denon's own search target,
// which is asked for directly: every other UPnP device on the network then
// stays out of the answer. Multicast does not survive every network, so a
// silent search falls back to knocking on the HEOS control port across the
// local subnet.
package discovery

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// ssdpMulticast is the standard SSDP address and port.
	ssdpMulticast = "239.255.255.250:1900"

	// heosTarget is Denon's search target for HEOS gear.
	heosTarget = "urn:schemas-denon-com:device:ACT-Denon:1"

	// CLIPort carries the HEOS control protocol. Reaching it is what makes a
	// device usable at all, so it doubles as the liveness check.
	CLIPort = "1255"

	// descriptionPort is where Denon gear serves its UPnP device description,
	// which is where a friendly name can be read.
	descriptionPort = "60006"

	// sweepTimeout is how long one host in the fallback sweep gets to answer.
	// A speaker on the same wire replies in milliseconds; waiting longer only
	// stretches the sweep across the addresses where nothing lives at all.
	sweepTimeout = 400 * time.Millisecond

	// sweepWidth is how many addresses the sweep has in flight at once, which
	// is what keeps a whole /24 inside a second.
	sweepWidth = 128
)

// Device is one speaker that answered.
type Device struct {
	Host      string // the IP address, without a port
	Name      string // friendly name, when the description could be read
	Model     string // model name, same caveat
	Reachable bool   // the HEOS control port accepted a connection
}

// String renders a device the way the tools report it to the user.
func (d Device) String() string {
	switch {
	case d.Name != "" && d.Model != "":
		return fmt.Sprintf("%s (%s, %s)", d.Host, d.Name, d.Model)
	case d.Name != "":
		return fmt.Sprintf("%s (%s)", d.Host, d.Name)
	default:
		return d.Host
	}
}

// Search returns every HEOS device that answers, best candidate first:
// devices whose control port is open sort ahead of ones that only answered
// SSDP, and address order breaks the tie so repeated runs agree.
func Search(ctx context.Context, timeout time.Duration) []Device {
	sources := localAddresses()
	if len(sources) == 0 {
		return nil
	}

	hosts := gather(ctx, sources, func(ctx context.Context, source net.IP) []string {
		return searchFrom(ctx, source, timeout)
	})
	if len(hosts) == 0 {
		// Multicast went nowhere — a guest VLAN, or a router that does not
		// forward it. The subnet is small enough to ask host by host.
		hosts = gather(ctx, sources, scanSubnet)
	}
	return describeAll(ctx, hosts)
}

// First is Search reduced to the single best answer, which is what a tool
// picking a speaker on its own actually wants.
func First(ctx context.Context, timeout time.Duration) (Device, bool) {
	found := Search(ctx, timeout)
	if len(found) == 0 {
		return Device{}, false
	}
	return found[0], true
}

// Reachable reports whether a HEOS device is listening at host. The port is
// the whole test: an address that does not accept a connection there cannot
// be controlled, whatever else it answers.
func Reachable(ctx context.Context, host string, timeout time.Duration) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, CLIPort))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// gather runs probe on every local address at once and merges the addresses
// it finds, keeping each host once.
func gather(ctx context.Context, sources []net.IP, probe func(context.Context, net.IP) []string) []string {
	var (
		mu    sync.Mutex
		seen  = map[string]bool{}
		hosts []string
		wg    sync.WaitGroup
	)
	for _, source := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, host := range probe(ctx, source) {
				mu.Lock()
				if !seen[host] {
					seen[host] = true
					hosts = append(hosts, host)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return hosts
}

// searchFrom sends an M-SEARCH from one local address and collects whoever
// answers before the timeout. Binding the socket to the address is what
// makes the request leave that particular interface.
func searchFrom(ctx context.Context, source net.IP, timeout time.Duration) []string {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: source})
	if err != nil {
		return nil
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	target, err := net.ResolveUDPAddr("udp4", ssdpMulticast)
	if err != nil {
		return nil
	}
	request := []byte("M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + ssdpMulticast + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + heosTarget + "\r\n" +
		"\r\n")
	// SSDP rides on UDP, where a lost datagram is ordinary. Three tries cost
	// nothing and make a speaker that misses the first one much rarer.
	for range 3 {
		if _, err := conn.WriteToUDP(request, target); err != nil {
			return nil
		}
	}

	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil
	}
	var hosts []string
	buffer := make([]byte, 2048)
	for {
		read, from, err := conn.ReadFromUDP(buffer)
		if err != nil {
			return hosts // the deadline, or ctx closing the socket
		}
		host, ok := replyHost(buffer[:read])
		if !ok {
			continue
		}
		if host == "" {
			host = from.IP.String()
		}
		hosts = append(hosts, host)
	}
}

// replyHost accepts a reply that names Denon's target and reports the address
// it advertises. The advertised LOCATION wins over the address the datagram
// came from, for firmware that answers from a different one.
func replyHost(reply []byte) (string, bool) {
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(reply)), nil)
	if err != nil {
		return "", false
	}
	defer func() { _ = response.Body.Close() }()
	denon := false
	for _, header := range []string{"St", "Usn", "Nt"} {
		if strings.Contains(response.Header.Get(header), "ACT-Denon") {
			denon = true
			break
		}
	}
	if !denon {
		return "", false
	}
	return hostFromLocation(response.Header.Get("Location")), true
}

// scanSubnet knocks on the HEOS port across the /24 around source. It is the
// fallback for networks that drop multicast, and never runs when SSDP already
// answered.
func scanSubnet(ctx context.Context, source net.IP) []string {
	v4 := source.To4()
	if v4 == nil {
		return nil
	}

	var (
		mu    sync.Mutex
		hosts []string
		wg    sync.WaitGroup
		slots = make(chan struct{}, sweepWidth)
	)
	for last := 1; last <= 254; last++ {
		if v4[3] == byte(last) {
			continue // ourselves
		}
		candidate := net.IPv4(v4[0], v4[1], v4[2], byte(last)).String()
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			if !Reachable(ctx, candidate, sweepTimeout) {
				return
			}
			mu.Lock()
			hosts = append(hosts, candidate)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return hosts
}

// describeAll fills in names and reachability for every host at once, then
// orders them so the best candidate comes first.
func describeAll(ctx context.Context, hosts []string) []Device {
	devices := make([]Device, len(hosts))
	var wg sync.WaitGroup
	for index, host := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			device := Device{Host: host}
			device.Reachable = Reachable(ctx, host, 2*time.Second)
			device.Name, device.Model = describe(ctx, host)
			devices[index] = device
		}()
	}
	wg.Wait()

	sort.SliceStable(devices, func(i, j int) bool {
		if devices[i].Reachable != devices[j].Reachable {
			return devices[i].Reachable
		}
		return bytes.Compare(net.ParseIP(devices[i].Host).To16(),
			net.ParseIP(devices[j].Host).To16()) < 0
	})
	return devices
}

var (
	friendlyNamePattern = regexp.MustCompile(`(?i)<friendlyName>([^<]*)</friendlyName>`)
	modelNamePattern    = regexp.MustCompile(`(?i)<modelName>([^<]*)</modelName>`)
)

// describe reads the speaker's own name out of its UPnP description. A device
// that will not describe itself is still perfectly usable, so failure here is
// two empty strings rather than an error.
func describe(ctx context.Context, host string) (name, model string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	address := "http://" + net.JoinHostPort(host, descriptionPort) +
		"/upnp/desc/aios_device/aios_device.xml"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", ""
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", ""
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", ""
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", ""
	}
	return parseDescription(raw)
}

// parseDescription pulls the names out of a UPnP device description.
func parseDescription(raw []byte) (name, model string) {
	if found := friendlyNamePattern.FindSubmatch(raw); found != nil {
		name = strings.TrimSpace(string(found[1]))
	}
	if found := modelNamePattern.FindSubmatch(raw); found != nil {
		model = strings.TrimSpace(string(found[1]))
	}
	return name, model
}

// hostFromLocation reads the address out of an SSDP LOCATION header, for
// firmware that answers from a different address than it advertises.
func hostFromLocation(location string) string {
	parsed, err := url.Parse(location)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// localAddresses lists the IPv4 addresses of every interface that is up and
// carries multicast, which is where a speaker could be.
func localAddresses() []net.IP {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var addresses []net.IP
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		if item.Flags&net.FlagMulticast == 0 {
			continue
		}
		found, err := item.Addrs()
		if err != nil {
			continue
		}
		for _, address := range found {
			network, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			if v4 := network.IP.To4(); v4 != nil && !v4.IsLoopback() {
				addresses = append(addresses, v4)
			}
		}
	}
	return addresses
}
