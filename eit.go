package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DVB EPG: the EIT (pid 18) present/following table carries programme
// titles, times and descriptions for every service on a mux. A background
// sweeper opens a light PSI-only session per mux and keeps a now/next
// store the info bar reads from.

type epgEvent struct {
	Start time.Time
	Dur   time.Duration
	Title string
	Text  string
}

var (
	epgMu   sync.RWMutex
	epgData = map[string][]epgEvent{} // freq|serviceID -> events
)

func epgKey(freq string, service int) string {
	return freq + "|" + fmt.Sprint(service)
}

// epgNowNext returns the running and upcoming programme for a service.
func epgNowNext(freq string, service int) (now, next *epgEvent) {
	epgMu.RLock()
	events := epgData[epgKey(freq, service)]
	epgMu.RUnlock()
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

// parseEIT extracts events from a present/following EIT section (0x4E).
func parseEIT(sec []byte) (service int, events []epgEvent) {
	if len(sec) < 18 || sec[0] != 0x4E {
		return 0, nil
	}
	slen := int(sec[1]&0x0F)<<8 | int(sec[2])
	if len(sec) < slen+3 {
		return 0, nil
	}
	service = int(sec[3])<<8 | int(sec[4])
	p := 14
	end := slen + 3 - 4 // strip CRC
	for p+12 <= end {
		start := dvbTime(sec[p+2 : p+7])
		dur := bcdDuration(sec[p+7 : p+10])
		dll := int(sec[p+10]&0x0F)<<8 | int(sec[p+11])
		desc := sec[p+12 : min(p+12+dll, end)]
		var title, text string
		for d := 0; d+2 <= len(desc); {
			tag, ln := desc[d], int(desc[d+1])
			body := desc[d+2 : min(d+2+ln, len(desc))]
			if tag == 0x4D && len(body) > 4 { // short_event_descriptor
				nl := int(body[3])
				if 4+nl <= len(body) {
					title = dvbText(body[4 : 4+nl])
					if 5+nl <= len(body) {
						tl := int(body[4+nl])
						if 5+nl+tl <= len(body) {
							text = dvbText(body[5+nl : 5+nl+tl])
						}
					}
				}
			}
			d += 2 + ln
		}
		if title != "" && !start.IsZero() {
			events = append(events, epgEvent{Start: start, Dur: dur, Title: title, Text: text})
		}
		p += 12 + dll
	}
	return service, events
}

// dvbTime decodes the 5-byte MJD + BCD UTC start time.
func dvbTime(b []byte) time.Time {
	if len(b) < 5 {
		return time.Time{}
	}
	mjd := int(b[0])<<8 | int(b[1])
	if mjd == 0xFFFF {
		return time.Time{}
	}
	yy := int((float64(mjd) - 15078.2) / 365.25)
	mm := int((float64(mjd) - 14956.1 - float64(int(float64(yy)*365.25))) / 30.6001)
	dd := mjd - 14956 - int(float64(yy)*365.25) - int(float64(mm)*30.6001)
	k := 0
	if mm == 14 || mm == 15 {
		k = 1
	}
	year, month, day := 1900+yy+k, mm-1-k*12, dd
	return time.Date(year, time.Month(month), day, bcd(b[2]), bcd(b[3]), bcd(b[4]), 0, time.UTC)
}

func bcd(b byte) int { return int(b>>4)*10 + int(b&0x0F) }

func bcdDuration(b []byte) time.Duration {
	if len(b) < 3 {
		return 0
	}
	return time.Duration(bcd(b[0]))*time.Hour + time.Duration(bcd(b[1]))*time.Minute + time.Duration(bcd(b[2]))*time.Second
}

// dvbText decodes a DVB string: UTF-8 marker (0x15) or Latin default.
func dvbText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if b[0] == 0x15 {
		return strings.ToValidUTF8(string(b[1:]), "")
	}
	if b[0] < 0x20 {
		b = b[1:] // other encoding tables; approximate as Latin below
	}
	// ISO 8859-15-ish fallback: bytes >= 0xA0 map close enough for Nordic
	// broadcasts; control range is dropped.
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c >= 0x20 && c < 0x7F:
			sb.WriteByte(c)
		case c >= 0xA0:
			sb.WriteRune(latin15[c-0xA0])
		}
	}
	return sb.String()
}

// ISO 8859-15 upper half.
var latin15 = [96]rune{
	' ', '¡', '¢', '£', '€', '¥', 'Š', '§', 'š', '©', 'ª', '«', '¬', '­', '®', '¯',
	'°', '±', '²', '³', 'Ž', 'µ', '¶', '·', 'ž', '¹', 'º', '»', 'Œ', 'œ', 'Ÿ', '¿',
	'À', 'Á', 'Â', 'Ã', 'Ä', 'Å', 'Æ', 'Ç', 'È', 'É', 'Ê', 'Ë', 'Ì', 'Í', 'Î', 'Ï',
	'Ð', 'Ñ', 'Ò', 'Ó', 'Ô', 'Õ', 'Ö', '×', 'Ø', 'Ù', 'Ú', 'Û', 'Ü', 'Ý', 'Þ', 'ß',
	'à', 'á', 'â', 'ã', 'ä', 'å', 'æ', 'ç', 'è', 'é', 'ê', 'ë', 'ì', 'í', 'î', 'ï',
	'ð', 'ñ', 'ò', 'ó', 'ô', 'õ', 'ö', '÷', 'ø', 'ù', 'ú', 'û', 'ü', 'ý', 'þ', 'ÿ',
}

// replacePIDs swaps the URL's pids parameter wholesale (for PSI-only
// probe sessions).
func replacePIDs(channelURL, pids string) string {
	i := strings.Index(channelURL, "pids=")
	if i < 0 {
		return channelURL
	}
	tail := channelURL[i+len("pids="):]
	rest := ""
	if j := strings.IndexByte(tail, '&'); j >= 0 {
		rest = tail[j:]
	}
	return channelURL[:i] + "pids=" + pids + rest
}

var (
	epgSweepMu   sync.Mutex
	epgSweepURLs []string
	epgSweepOnce sync.Once
	epgSweepKick = make(chan struct{}, 1)
)

// startEPGSweeper (re)sets the mux representative URLs and ensures the
// background sweep loop runs: an immediate sweep, then every 10 minutes.
func startEPGSweeper(muxURLs []string) {
	epgSweepMu.Lock()
	epgSweepURLs = muxURLs
	epgSweepMu.Unlock()
	select {
	case epgSweepKick <- struct{}{}:
	default:
	}
	epgSweepOnce.Do(func() {
		go func() {
			t := time.NewTicker(10 * time.Minute)
			defer t.Stop()
			for {
				select {
				case <-epgSweepKick:
				case <-t.C:
				}
				epgSweepMu.Lock()
				urls := append([]string(nil), epgSweepURLs...)
				epgSweepMu.Unlock()
				for _, u := range urls {
					// Serialize with the pid-prefetch worker: the box has a
					// limited tuner budget and playback comes first.
					expandSem <- struct{}{}
					epgSweepMux(u)
					<-expandSem
				}
			}
		}()
	})
}

// sweepMuxNow runs a prioritized EPG sweep of one mux in the background
// (e.g. the mux just tuned), so its now/next is available within seconds
// without waiting for the periodic full sweep.
func sweepMuxNow(muxURL string) {
	if muxURL == "" {
		return
	}
	go func() {
		expandSem <- struct{}{}
		defer func() { <-expandSem }()
		epgSweepMux(muxURL)
	}()
}

// epgSweepMux opens a PSI-only session on the mux and collects EIT
// present/following events for all its services.
func epgSweepMux(muxURL string) {
	s, err := dialSatIPBytes(replacePIDs(muxURL, "0,17,18"), 4*1024)
	if err != nil {
		log.Printf("epg sweep %s: %v", muxURL, err)
		return
	}
	s.psiOnly = true
	defer s.Close()

	freq := urlParam(muxURL, "freq")
	var asm sectionAssembler
	updated := map[string][]epgEvent{}
	off := s.ring.Start()
	buf := make([]byte, 32*1024)
	pending := []byte{}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, newOff, ok := s.ring.ReadFrom(off, buf)
		if !ok {
			break
		}
		off = newOff
		pending = append(pending, buf[:n]...)
		for len(pending) >= 188 {
			pkt := pending[:188]
			pending = pending[188:]
			if tsPID(pkt) != 18 {
				continue
			}
			sec := asm.feed(pkt)
			if sec == nil {
				continue
			}
			if svc, events := parseEIT(sec); len(events) > 0 {
				key := epgKey(freq, svc)
				updated[key] = mergeEvents(updated[key], events)
			}
		}
	}
	if len(updated) == 0 {
		return
	}
	epgMu.Lock()
	for k, v := range updated {
		epgData[k] = v
	}
	epgMu.Unlock()
	log.Printf("epg: mux %s -> %d services", freq, len(updated))
	saveEPGCache()
}

// EPG persistence: keep now/next data across restarts so the info bar has
// something to show immediately instead of blank space until the first
// sweep completes.

var epgSaveMu sync.Mutex // serializes concurrent file writes

func epgCachePath() (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "epg.json"), nil
}

// loadEPGCache restores persisted events, dropping any that already ended.
func loadEPGCache() {
	path, err := epgCachePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	stored := map[string][]epgEvent{}
	if json.Unmarshal(data, &stored) != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	n := 0
	epgMu.Lock()
	for key, events := range stored {
		var live []epgEvent
		for _, e := range events {
			if e.Start.Add(e.Dur).After(cutoff) {
				live = append(live, e)
			}
		}
		if len(live) > 0 {
			epgData[key] = live
			n += len(live)
		}
	}
	epgMu.Unlock()
	log.Printf("epg cache: %d events across %d services loaded", n, len(stored))
}

// saveEPGCache atomically writes the current store, pruning long-past events.
func saveEPGCache() {
	epgSaveMu.Lock()
	defer epgSaveMu.Unlock()
	path, err := epgCachePath()
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	snapshot := map[string][]epgEvent{}
	epgMu.RLock()
	for key, events := range epgData {
		var live []epgEvent
		for _, e := range events {
			if e.Start.Add(e.Dur).After(cutoff) {
				live = append(live, e)
			}
		}
		if len(live) > 0 {
			snapshot[key] = live
		}
	}
	epgMu.RUnlock()
	data, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func mergeEvents(existing, add []epgEvent) []epgEvent {
	seen := map[string]bool{}
	for _, e := range existing {
		seen[e.Title+e.Start.String()] = true
	}
	for _, e := range add {
		if !seen[e.Title+e.Start.String()] {
			existing = append(existing, e)
		}
	}
	sort.Slice(existing, func(i, j int) bool { return existing[i].Start.Before(existing[j].Start) })
	return existing
}

// urlParam extracts a raw query parameter value.
func urlParam(rawurl, key string) string {
	i := strings.Index(rawurl, key+"=")
	if i < 0 {
		return ""
	}
	v := rawurl[i+len(key)+1:]
	if j := strings.IndexByte(v, '&'); j >= 0 {
		v = v[:j]
	}
	return v
}
