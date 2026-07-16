package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Channel is one entry from an m3u playlist.
type Channel struct {
	Name     string
	URL      string
	Category string // group-title attribute (first segment), if any
	Country  string // ISO 3166 alpha-2 from the tvg-id suffix, if any
	Local    bool   // served from the local network (discovered Fritz!Box)
}

// parseM3U parses an extended m3u playlist. Fritz!Box lists look like:
//
//	#EXTM3U
//	#EXTINF:0,Das Erste HD
//	rtsp://192.168.178.1:554/?avm=1&freq=330&...
//
// Attribute-style EXTINF lines (tvg-id, group-title, ...) are tolerated;
// the display name is everything after the last comma outside quotes.
func parseM3U(r io.Reader) ([]Channel, error) {
	var channels []Channel
	var pending Channel

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line == "#EXTM3U" || strings.HasPrefix(line, "#EXTM3U "):
			continue
		case strings.HasPrefix(line, "#EXTINF:"):
			pending = Channel{Name: extinfName(line)}
			attrs := extinfAttrs(line)
			if g := attrs["group-title"]; g != "" {
				// iptv-org uses a single category; be tolerant of lists
				// that pack several separated by semicolons.
				pending.Category = strings.TrimSpace(strings.SplitN(g, ";", 2)[0])
			}
			pending.Country = tvgIDCountry(attrs["tvg-id"])
		case strings.HasPrefix(line, "#"):
			continue // other directives
		default:
			c := pending
			c.URL = line
			if c.Name == "" {
				c.Name = line
			}
			channels = append(channels, c)
			pending = Channel{}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("no channels found in playlist")
	}
	return channels, nil
}

// extinfName extracts the display name from an #EXTINF line: the text after
// the first comma that is not inside double quotes (attribute values may
// contain commas).
func extinfName(line string) string {
	rest := strings.TrimPrefix(line, "#EXTINF:")
	inQuotes := false
	for i, r := range rest {
		switch r {
		case '"':
			inQuotes = !inQuotes
		case ',':
			if !inQuotes {
				return strings.TrimSpace(rest[i+1:])
			}
		}
	}
	return ""
}

// extinfAttrs extracts key="value" attributes from an #EXTINF line.
func extinfAttrs(line string) map[string]string {
	attrs := map[string]string{}
	for _, m := range extinfAttrRe.FindAllStringSubmatch(line, -1) {
		attrs[m[1]] = m[2]
	}
	return attrs
}

var extinfAttrRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9-]*)="([^"]*)"`)

// tvgIDCountry pulls the country code out of iptv-org style tvg-ids like
// "YleTV1.fi" or "SomeChannel.us@East".
func tvgIDCountry(id string) string {
	if m := tvgCountryRe.FindStringSubmatch(id); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

var tvgCountryRe = regexp.MustCompile(`\.([a-z]{2})(?:@|$)`)

func cacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "kabel"), nil
}

// loadChannels fetches the playlist from url, caching the raw bytes on
// success. If the fetch fails it falls back to the last cached copy so the
// app still starts away from the Fritz!Box network.
func loadChannels(url string) ([]Channel, error) {
	data, fetchErr := fetchM3U(url)
	if fetchErr == nil {
		cacheM3U(data)
		return parseM3U(strings.NewReader(string(data)))
	}

	if dir, err := cacheDir(); err == nil {
		if cached, err := os.ReadFile(filepath.Join(dir, "tv.m3u")); err == nil {
			channels, err := parseM3U(strings.NewReader(string(cached)))
			if err == nil {
				return channels, nil
			}
		}
	}
	return nil, fmt.Errorf("fetching %s: %w (and no usable cached playlist)", url, fetchErr)
}

func cacheM3U(data []byte) {
	if dir, err := cacheDir(); err == nil {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "tv.m3u"), data, 0o644)
		}
	}
}

// m3uClient returns an HTTP client for fetching a playlist. For private
// LAN hosts (and .box names) it tolerates self-signed certificates, since
// Fritz!Boxes redirect HTTP to HTTPS with one; public hosts stay strict.
func m3uClient(rawurl string, timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	if u, err := neturl.Parse(rawurl); err == nil && privateHost(u.Hostname()) {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	return client
}

func privateHost(host string) bool {
	if strings.HasSuffix(host, ".box") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func fetchM3U(url string) ([]byte, error) {
	client := m3uClient(url, 5*time.Second)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
}
