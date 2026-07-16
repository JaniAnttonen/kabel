package main

import (
	"strings"
	"testing"
)

const fritzBoxFixture = `#EXTM3U
#EXTINF:0,Das Erste HD
rtsp://192.168.178.1:554/?avm=1&freq=330&bw=8&msys=dvbc&mtype=256qam&sr=6900&specinv=1&pids=0,16,17,18,20,5100,5101,5102,5103,5106
#EXTINF:0,ZDF HD
rtsp://192.168.178.1:554/?avm=1&freq=450&bw=8&msys=dvbc&mtype=256qam&sr=6900&specinv=1&pids=0,16,17,18,20,6100,6110,6120,6121,6123
#EXTINF:0,arte HD
rtsp://192.168.178.1:554/?avm=1&freq=330&bw=8&msys=dvbc&mtype=256qam&sr=6900&specinv=1&pids=0,16,17,18,20,5200,5201,5202,5203
`

func TestParseFritzBoxPlaylist(t *testing.T) {
	channels, err := parseM3U(strings.NewReader(fritzBoxFixture))
	if err != nil {
		t.Fatalf("parseM3U: %v", err)
	}
	if len(channels) != 3 {
		t.Fatalf("got %d channels, want 3", len(channels))
	}
	if channels[0].Name != "Das Erste HD" {
		t.Errorf("channel 0 name = %q, want %q", channels[0].Name, "Das Erste HD")
	}
	if !strings.HasPrefix(channels[1].URL, "rtsp://192.168.178.1:554/?avm=1&freq=450") {
		t.Errorf("channel 1 URL = %q", channels[1].URL)
	}
	if channels[2].Name != "arte HD" {
		t.Errorf("channel 2 name = %q, want %q", channels[2].Name, "arte HD")
	}
}

func TestParseAttributeStyleExtinf(t *testing.T) {
	src := `#EXTM3U
#EXTINF:-1 tvg-id="daserste.de" group-title="Vollprogramm, HD",Das Erste HD
http://example.com/stream1
#EXTINF:0,
http://example.com/stream2
http://example.com/bare-url
`
	channels, err := parseM3U(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parseM3U: %v", err)
	}
	if len(channels) != 3 {
		t.Fatalf("got %d channels, want 3", len(channels))
	}
	// Comma inside quoted attribute must not truncate the name.
	if channels[0].Name != "Das Erste HD" {
		t.Errorf("channel 0 name = %q, want %q", channels[0].Name, "Das Erste HD")
	}
	// Empty EXTINF name falls back to the URL.
	if channels[1].Name != "http://example.com/stream2" {
		t.Errorf("channel 1 name = %q", channels[1].Name)
	}
	// Bare URL with no EXTINF at all.
	if channels[2].URL != "http://example.com/bare-url" {
		t.Errorf("channel 2 URL = %q", channels[2].URL)
	}
}

func TestParseEmptyPlaylist(t *testing.T) {
	if _, err := parseM3U(strings.NewReader("#EXTM3U\n")); err == nil {
		t.Fatal("expected error for playlist with no channels")
	}
}
