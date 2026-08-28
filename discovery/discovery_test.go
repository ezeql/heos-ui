package discovery

import "testing"

// A reply captured from a Denon Home 150 on the local network.
const denonReply = "HTTP/1.1 200 OK\r\n" +
	"CACHE-CONTROL: max-age=1800\r\n" +
	"EXT:\r\n" +
	"LOCATION: http://192.168.101.6:60006/upnp/desc/aios_device/aios_device.xml\r\n" +
	"ST: urn:schemas-denon-com:device:ACT-Denon:1\r\n" +
	"USN: uuid:f5f7fb34-2931-20b7-b67c-b1a52a4ec43e::urn:schemas-denon-com:device:ACT-Denon:1\r\n" +
	"\r\n"

func TestADenonReplyIsRecognized(t *testing.T) {
	host, ok := replyHost([]byte(denonReply))
	if !ok {
		t.Fatal("a Denon reply was not recognized")
	}
	if host != "192.168.101.6" {
		t.Errorf("host = %q, want 192.168.101.6", host)
	}
}

func TestAnotherVendorsReplyIsIgnored(t *testing.T) {
	reply := "HTTP/1.1 200 OK\r\n" +
		"LOCATION: http://192.168.101.9:1400/xml/device_description.xml\r\n" +
		"ST: urn:schemas-upnp-org:device:ZonePlayer:1\r\n" +
		"USN: uuid:RINCON_000E58::urn:schemas-upnp-org:device:ZonePlayer:1\r\n" +
		"\r\n"
	if _, ok := replyHost([]byte(reply)); ok {
		t.Error("a Sonos reply was taken for a HEOS speaker")
	}
}

func TestAGarbledReplyIsIgnored(t *testing.T) {
	if _, ok := replyHost([]byte("not http at all")); ok {
		t.Error("garbage was taken for a reply")
	}
}

// Some firmware answers from one address and advertises another; the one it
// advertises is where its services actually are.
func TestTheAdvertisedAddressWins(t *testing.T) {
	host, ok := replyHost([]byte(denonReply))
	if !ok {
		t.Fatal("the reply was not recognized")
	}
	if host != hostFromLocation("http://192.168.101.6:60006/upnp/desc/aios_device/aios_device.xml") {
		t.Errorf("host = %q, want the address from LOCATION", host)
	}
}

// A reply with no LOCATION still counts; the caller falls back to the address
// the datagram came from.
func TestAReplyWithoutALocationStillCounts(t *testing.T) {
	reply := "HTTP/1.1 200 OK\r\n" +
		"ST: urn:schemas-denon-com:device:ACT-Denon:1\r\n\r\n"
	host, ok := replyHost([]byte(reply))
	if !ok {
		t.Fatal("a Denon reply without a location was rejected")
	}
	if host != "" {
		t.Errorf("host = %q, want empty so the sender address is used", host)
	}
}

func TestANameIsReadFromTheDescription(t *testing.T) {
	description := []byte(`<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0">` +
		`<device><deviceType>urn:schemas-upnp-org:device:MediaRenderer:1</deviceType>` +
		`<friendlyName>Kitchen</friendlyName><manufacturer>Denon</manufacturer>` +
		`<modelName>Denon Home 150</modelName></device></root>`)
	name, model := parseDescription(description)
	if name != "Kitchen" {
		t.Errorf("name = %q, want Kitchen", name)
	}
	if model != "Denon Home 150" {
		t.Errorf("model = %q, want Denon Home 150", model)
	}
}

// A speaker that will not describe itself is still perfectly usable.
func TestADescriptionWithoutNamesIsEmpty(t *testing.T) {
	name, model := parseDescription([]byte(`<root><device/></root>`))
	if name != "" || model != "" {
		t.Errorf("got %q/%q, want both empty", name, model)
	}
}

func TestADeviceReadsAsItsNameAndAddress(t *testing.T) {
	cases := []struct {
		device Device
		want   string
	}{
		{
			Device{Host: "10.0.0.4", Name: "Kitchen", Model: "Denon Home 150"},
			"10.0.0.4 (Kitchen, Denon Home 150)",
		},
		{Device{Host: "10.0.0.4", Name: "Kitchen"}, "10.0.0.4 (Kitchen)"},
		{Device{Host: "10.0.0.4"}, "10.0.0.4"},
	}
	for _, item := range cases {
		if got := item.device.String(); got != item.want {
			t.Errorf("String() = %q, want %q", got, item.want)
		}
	}
}
