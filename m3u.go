package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Channel is one entry from the Fritz!Box m3u playlist.
type Channel struct {
	Name string
	URL  string
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
	var pendingName string

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line == "#EXTM3U" || strings.HasPrefix(line, "#EXTM3U "):
			continue
		case strings.HasPrefix(line, "#EXTINF:"):
			pendingName = extinfName(line)
		case strings.HasPrefix(line, "#"):
			continue // other directives
		default:
			name := pendingName
			if name == "" {
				name = line
			}
			channels = append(channels, Channel{Name: name, URL: line})
			pendingName = ""
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

func fetchM3U(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Second}
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
