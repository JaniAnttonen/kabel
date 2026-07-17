package main

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// XMLTV EPG fallback: DVB EIT (broadcast) is authoritative but only covers
// services on muxes we can sweep and only now/next. A public XMLTV guide
// fills channels the EIT misses and survives the box being unreachable.

// defaultXMLTVURL is a community-hosted Finnish XMLTV guide (gzipped).
const defaultXMLTVURL = "https://epgshare01.online/epgshare01/epg_ripper_FI1.xml.gz"

var (
	xmltvMu   sync.RWMutex
	xmltvData = map[string][]epgEvent{} // normalized channel name -> events
)

// normalizeChannelName reduces a channel name to letters+digits, lowercased,
// with a trailing "hd" removed, so "Yle TV1 HD" and "YLE TV1" both key to
// "yletv1".
func normalizeChannelName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSuffix(b.String(), "hd")
}

// xmltvNowNext returns the running and upcoming programme for a channel name
// from the XMLTV store ("" results if unknown).
func xmltvNowNext(channelName string) (now, next *epgEvent) {
	key := normalizeChannelName(channelName)
	xmltvMu.RLock()
	events := xmltvData[key]
	xmltvMu.RUnlock()
	t := time.Now()
	for i := range events {
		e := &events[i]
		if !t.Before(e.Start) && t.Before(e.Start.Add(e.Dur)) {
			now = e
		} else if e.Start.After(t) && (next == nil || e.Start.Before(next.Start)) {
			next = e
		}
	}
	return now, next
}

func xmltvCachePath() (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "epg_xmltv.xml.gz"), nil
}

// startXMLTV fetches the guide on launch and every 6h, falling back to a
// cached copy when the network is unavailable.
func startXMLTV(url string) {
	go func() {
		refresh := func() {
			if data, err := fetchXMLTV(url); err == nil {
				if parseXMLTV(data) == nil {
					cacheXMLTV(data)
					return
				}
			} else {
				log.Printf("xmltv fetch: %v", err)
			}
			if data, err := loadCachedXMLTV(); err == nil {
				_ = parseXMLTV(data)
			}
		}
		refresh()
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for range t.C {
			refresh()
		}
	}()
}

func fetchXMLTV(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, io.EOF
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
}

func cacheXMLTV(data []byte) {
	if path, err := xmltvCachePath(); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, data, 0o644)
	}
}

func loadCachedXMLTV() ([]byte, error) {
	path, err := xmltvCachePath()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// parseXMLTV decodes a (gzipped) XMLTV document into the name-keyed store,
// keeping only events within a useful window.
func parseXMLTV(gz []byte) error {
	var r io.Reader = bytes.NewReader(gz)
	if zr, err := gzip.NewReader(bytes.NewReader(gz)); err == nil {
		r = zr
		defer zr.Close()
	}

	dec := xml.NewDecoder(r)
	idNames := map[string][]string{} // channel id -> normalized names
	built := map[string][]epgEvent{}
	windowStart := time.Now().Add(-3 * time.Hour)
	windowEnd := time.Now().Add(48 * time.Hour)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "channel":
			var ch struct {
				ID    string   `xml:"id,attr"`
				Names []string `xml:"display-name"`
			}
			if dec.DecodeElement(&ch, &se) == nil {
				var keys []string
				for _, n := range ch.Names {
					if k := normalizeChannelName(n); k != "" {
						keys = append(keys, k)
					}
				}
				idNames[ch.ID] = keys
			}
		case "programme":
			var pr struct {
				Start   string `xml:"start,attr"`
				Stop    string `xml:"stop,attr"`
				Channel string `xml:"channel,attr"`
				Title   string `xml:"title"`
				Desc    string `xml:"desc"`
			}
			if dec.DecodeElement(&pr, &se) != nil {
				continue
			}
			start := parseXMLTVTime(pr.Start)
			stop := parseXMLTVTime(pr.Stop)
			if start.IsZero() || stop.Before(start) || stop.Before(windowStart) || start.After(windowEnd) {
				continue
			}
			ev := epgEvent{Start: start, Dur: stop.Sub(start), Title: pr.Title, Text: pr.Desc}
			for _, key := range idNames[pr.Channel] {
				built[key] = append(built[key], ev)
			}
		}
	}

	if len(built) == 0 {
		return nil
	}
	for _, evs := range built {
		sort.Slice(evs, func(i, j int) bool { return evs[i].Start.Before(evs[j].Start) })
	}
	xmltvMu.Lock()
	xmltvData = built
	xmltvMu.Unlock()
	log.Printf("xmltv: %d channels loaded", len(built))
	return nil
}

// parseXMLTVTime parses "20260717032500 +0000".
func parseXMLTVTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"20060102150405 -0700", "20060102150405"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
