package position

// The speaker's UPnP renderer reports a position even for URL streams, where
// HEOS reports nothing at all. One SOAP call, no dependencies.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// upnpPort is where Denon/HEOS gear serves its UPnP services.
const upnpPort = "60006"

// defaultControlPath is standard on Denon/HEOS gear; the device description
// is consulted first regardless.
const defaultControlPath = "/upnp/control/renderer_dvc/AVTransport"

const envelopeOpen = `<?xml version="1.0"?>` +
	`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" ` +
	`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`

const envelopeClose = `</s:Body></s:Envelope>`

const positionInfoBody = `<u:GetPositionInfo xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">` +
	`<InstanceID>0</InstanceID></u:GetPositionInfo>`

// Measured on a Denon Home 150: its AVTransport advertises seek modes
// TRACK_NR and REL_TIME, so a position can be given as a wall-clock offset.
const seekBody = `<u:Seek xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">` +
	`<InstanceID>0</InstanceID><Unit>REL_TIME</Unit><Target>%s</Target></u:Seek>`

var (
	controlURLPattern = regexp.MustCompile(`(?si)AVTransport:1</serviceType>.*?<controlURL>([^<]+)`)
	relTimePattern    = regexp.MustCompile(`(?i)<RelTime>([^<]*)</RelTime>`)
)

// UPnP asks the renderer how far into the current track it is. The second
// result is false when the speaker did not answer or does not know — a
// stopped renderer legitimately sits at zero, so zero comes back as unknown
// and the caller falls back to its own clock, which is right in both cases.
func UPnP(ctx context.Context, host string, timeout time.Duration) (float64, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{}
	base := "http://" + net.JoinHostPort(host, upnpPort)
	body, ok := soapCall(ctx, client, base, "GetPositionInfo", positionInfoBody)
	if !ok {
		return 0, false
	}
	return parseRelTime(body)
}

// Seek jumps to position seconds into the current track, reporting whether
// the speaker took it. The HEOS protocol has no seek at all; the speaker's
// UPnP renderer is the only route to moving within a track.
func Seek(ctx context.Context, host string, seconds float64, timeout time.Duration) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{}
	base := "http://" + net.JoinHostPort(host, upnpPort)
	body := fmt.Sprintf(seekBody, asClock(seconds))
	answer, ok := soapCall(ctx, client, base, "Seek", body)
	if !ok {
		return false
	}
	// A SOAP fault comes back with status 200 and an error body, so a reply
	// on its own does not mean the seek happened.
	return !strings.Contains(answer, "UPnPError") && !strings.Contains(answer, "s:Fault")
}

// asClock renders seconds as h:mm:ss, which is what REL_TIME expects.
func asClock(seconds float64) string {
	total := max(int(seconds), 0)
	return fmt.Sprintf("%d:%02d:%02d", total/3600, total/60%60, total%60)
}

func soapCall(ctx context.Context, client *http.Client, base, action, body string) (string, bool) {
	url := base + controlPath(ctx, client, base)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(envelopeOpen+body+envelopeClose))
	if err != nil {
		return "", false
	}
	request.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	request.Header.Set("SOAPAction", `"urn:schemas-upnp-org:service:AVTransport:1#`+action+`"`)
	response, err := client.Do(request)
	if err != nil {
		return "", false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// controlPath reads the AVTransport control path from the device description,
// falling back to the Denon default.
func controlPath(ctx context.Context, client *http.Client, base string) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/upnp/desc/aios_device/aios_device.xml", nil)
	if err != nil {
		return defaultControlPath
	}
	response, err := client.Do(request)
	if err != nil {
		return defaultControlPath
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return defaultControlPath
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return defaultControlPath
	}
	if found := controlURLPattern.FindSubmatch(raw); found != nil {
		return string(found[1])
	}
	return defaultControlPath
}

// parseRelTime reads seconds from the RelTime field. Devices that do not
// track position answer with NOT_IMPLEMENTED or with zeroes.
func parseRelTime(xml string) (float64, bool) {
	match := relTimePattern.FindStringSubmatch(xml)
	if match == nil {
		return 0, false
	}
	parts := strings.Split(strings.TrimSpace(match[1]), ":")
	if len(parts) != 3 {
		return 0, false
	}
	total := 0.0
	for _, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, false
		}
		total = total*60 + value
	}
	if total == 0 {
		return 0, false
	}
	return total, true
}

// Current returns how far into the current track playback is, in seconds,
// and whether the answer is an estimate. reportedMS is a fresh position
// reported by HEOS itself in milliseconds (zero when it reported nothing,
// which is every URL stream). Whenever HEOS or UPnP answers, the clock is
// re-anchored to it, so the estimate stays close even between real readings.
func Current(ctx context.Context, reportedMS int, clock *Clock, host string, playing bool) (seconds float64, estimated, ok bool) {
	if reportedMS > 0 {
		reported := float64(reportedMS) / 1000
		if clock != nil {
			clock.Anchor(reported)
		}
		return reported, false, true
	}
	if host != "" {
		if real, found := UPnP(ctx, host, 5*time.Second); found {
			if clock != nil {
				clock.Anchor(real)
			}
			return real, false, true
		}
	}
	if clock == nil {
		return 0, true, false
	}
	clock.Sync(playing)
	return clock.Position(), true, true
}
