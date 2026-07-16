package main

import (
	"strings"
	"testing"
)

func TestArrangeChannelsLocalFirstAndHomeCountry(t *testing.T) {
	local := []Channel{
		{Name: "Das Erste HD", URL: "rtsp://box/1", Local: true},
		{Name: "ZDF HD", URL: "rtsp://box/2", Local: true},
	}
	public := []Channel{
		{Name: "CNN", URL: "http://x/cnn", Category: "News", Country: "US"},
		{Name: "Yle Uutiset", URL: "http://x/yle", Category: "News", Country: "FI"},
		{Name: "Weird TV", URL: "http://x/weird"},
		{Name: "Animal Planet", URL: "http://x/ap", Category: "Animation", Country: "US"},
	}
	channels, items := arrangeChannels(local, public, "FI")

	if len(channels) != 6 {
		t.Fatalf("merged %d channels, want 6", len(channels))
	}
	// Local pinned on top after its header.
	if items[0].header != "Local — Fritz!Box" {
		t.Fatalf("first item = %+v, want local header", items[0])
	}
	if channels[items[1].chanIdx].Name != "Das Erste HD" {
		t.Errorf("first channel = %s, want Das Erste HD", channels[items[1].chanIdx].Name)
	}

	var headers []string
	var order []string
	for _, it := range items {
		if it.header != "" {
			headers = append(headers, it.header)
		} else {
			order = append(order, channels[it.chanIdx].Name)
		}
	}
	wantHeaders := []string{"Local — Fritz!Box", "Animation", "News", undefinedCategory}
	if strings.Join(headers, "|") != strings.Join(wantHeaders, "|") {
		t.Errorf("headers = %v, want %v", headers, wantHeaders)
	}
	// Home country (FI) first within News despite catalog order.
	wantOrder := []string{"Das Erste HD", "ZDF HD", "Animal Planet", "Yle Uutiset", "CNN", "Weird TV"}
	if strings.Join(order, "|") != strings.Join(wantOrder, "|") {
		t.Errorf("order = %v, want %v", order, wantOrder)
	}
}

func TestArrangeChannelsPlainListNoHeaders(t *testing.T) {
	public := []Channel{
		{Name: "One", URL: "rtsp://box/1"},
		{Name: "Two", URL: "rtsp://box/2"},
	}
	_, items := arrangeChannels(nil, public, "FI")
	for _, it := range items {
		if it.header != "" {
			t.Fatalf("unexpected header %q in single-group list", it.header)
		}
	}
}

func TestTvgIDCountry(t *testing.T) {
	cases := map[string]string{
		"YleTV1.fi":           "FI",
		"SomeChannel.us@East": "US",
		"NoCountry":           "",
		"":                    "",
	}
	for in, want := range cases {
		if got := tvgIDCountry(in); got != want {
			t.Errorf("tvgIDCountry(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestZoneTabCountry(t *testing.T) {
	tab := "# comment\nFI\t+6010+02458\tEurope/Helsinki\nDE\t+5230+01322\tEurope/Berlin\n"
	if got := zoneTabCountry(strings.NewReader(tab), "Europe/Helsinki"); got != "FI" {
		t.Errorf("zoneTabCountry = %q, want FI", got)
	}
	if got := zoneTabCountry(strings.NewReader(tab), "Mars/Olympus"); got != "" {
		t.Errorf("zoneTabCountry unknown zone = %q, want empty", got)
	}
}

func TestPrivateHost(t *testing.T) {
	cases := map[string]bool{
		"fritz.box":            true,
		"192.168.178.1":        true,
		"10.0.0.5":             true,
		"iptv-org.github.io":   false,
		"212.42.244.122":       false,
		"myrouter.example.com": false,
	}
	for host, want := range cases {
		if got := privateHost(host); got != want {
			t.Errorf("privateHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestSSDPLocationHost(t *testing.T) {
	resp := "HTTP/1.1 200 OK\r\nEXT:\r\nLOCATION: http://192.168.178.1:49000/satipdesc.xml\r\nST: urn:ses-com:device:SatIPServer:1\r\n\r\n"
	if got := ssdpLocationHost(resp); got != "192.168.178.1" {
		t.Errorf("ssdpLocationHost = %q, want 192.168.178.1", got)
	}
}
