package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Fritz!Box only forwards the PIDs listed in the channel URL, and its
// playlist omits DVB subtitle (and some audio) streams. We parse the
// channel's PAT/PMT once and re-request with every elementary stream PID,
// which makes all subtitle/audio tracks actually receivable.

// sectionAssembler collects one PSI section that may span TS packets.
type sectionAssembler struct {
	buf     []byte
	started bool
}

// feed consumes one 188-byte TS packet and returns the completed section,
// or nil while incomplete.
func (a *sectionAssembler) feed(pkt []byte) []byte {
	if len(pkt) < 188 || pkt[0] != 0x47 {
		return nil
	}
	pusi := pkt[1]&0x40 != 0
	p := 4
	if (pkt[3]>>4)&2 != 0 { // adaptation field
		p += 1 + int(pkt[p])
		if p >= 188 {
			return nil
		}
	}
	if pusi {
		ptr := int(pkt[p])
		p += 1 + ptr
		if p >= 188 {
			return nil
		}
		a.buf = append(a.buf[:0], pkt[p:188]...)
		a.started = true
	} else if a.started {
		a.buf = append(a.buf, pkt[p:188]...)
	} else {
		return nil
	}
	if len(a.buf) >= 3 {
		slen := int(a.buf[1]&0x0F)<<8 | int(a.buf[2])
		if len(a.buf) >= slen+3 {
			return a.buf[:slen+3]
		}
	}
	return nil
}

func tsPID(pkt []byte) int { return int(pkt[1]&0x1F)<<8 | int(pkt[2]) }

// parsePAT returns all programs' PMT PIDs. A DVB mux carries many services,
// so the caller must pick the one belonging to its channel.
func parsePAT(sec []byte) []int {
	if len(sec) < 12 || sec[0] != 0x00 {
		return nil
	}
	var pids []int
	end := len(sec) - 4 // strip CRC
	for p := 8; p+4 <= end; p += 4 {
		prog := int(sec[p])<<8 | int(sec[p+1])
		pid := int(sec[p+2]&0x1F)<<8 | int(sec[p+3])
		if prog != 0 { // 0 = network PID
			pids = append(pids, pid)
		}
	}
	return pids
}

// urlPIDSet parses the pids= parameter of a channel URL.
func urlPIDSet(channelURL string) map[int]bool {
	set := map[int]bool{}
	i := strings.Index(channelURL, "pids=")
	if i < 0 {
		return set
	}
	tail := channelURL[i+len("pids="):]
	if j := strings.IndexByte(tail, '&'); j >= 0 {
		tail = tail[:j]
	}
	for _, f := range strings.Split(tail, ",") {
		if v, err := strconv.Atoi(f); err == nil {
			set[v] = true
		}
	}
	return set
}

// parsePMTPIDs returns the PCR PID and every elementary stream PID.
func parsePMTPIDs(sec []byte) []int {
	if len(sec) < 16 || sec[0] != 0x02 {
		return nil
	}
	var pids []int
	if pcr := int(sec[8]&0x1F)<<8 | int(sec[9]); pcr != 0x1FFF {
		pids = append(pids, pcr)
	}
	infoLen := int(sec[10]&0x0F)<<8 | int(sec[11])
	p := 12 + infoLen
	end := len(sec) - 4
	for p+5 <= end {
		pids = append(pids, int(sec[p+1]&0x1F)<<8|int(sec[p+2]))
		p += 5 + int(sec[p+3]&0x0F)<<8 + int(sec[p+4])
	}
	return pids
}

// mergePIDsIntoURL unions extra pids into the URL's pids= parameter,
// preserving the parameter order the box expects.
func mergePIDsIntoURL(channelURL string, extra []int) string {
	i := strings.Index(channelURL, "pids=")
	if i < 0 {
		return channelURL
	}
	tail := channelURL[i+len("pids="):]
	rest := ""
	if j := strings.IndexByte(tail, '&'); j >= 0 {
		rest = tail[j:]
		tail = tail[:j]
	}
	seen := map[int]bool{}
	var pids []int
	for _, f := range strings.Split(tail, ",") {
		if v, err := strconv.Atoi(f); err == nil && !seen[v] {
			seen[v] = true
			pids = append(pids, v)
		}
	}
	for _, v := range extra {
		if !seen[v] {
			seen[v] = true
			pids = append(pids, v)
		}
	}
	sort.Ints(pids)
	strs := make([]string, len(pids))
	for k, v := range pids {
		strs[k] = strconv.Itoa(v)
	}
	return channelURL[:i] + "pids=" + strings.Join(strs, ",") + rest
}

var pidExpandCache sync.Map // channel URL -> expanded URL
var progNumCache sync.Map   // channel URL -> DVB service id (program number)

// The expansion results are stable per channel, so they're persisted —
// warm starts skip ~20 probe sessions and free the tuner budget for the
// EPG sweep right away.

type pidCacheEntry struct {
	Expanded string `json:"expanded"`
	Service  int    `json:"service"`
}

func pidCachePath() (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pids.json"), nil
}

func loadPIDCache() {
	path, err := pidCachePath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	entries := map[string]pidCacheEntry{}
	if json.Unmarshal(data, &entries) != nil {
		return
	}
	for u, e := range entries {
		pidExpandCache.Store(u, e.Expanded)
		if e.Service > 0 {
			progNumCache.Store(u, e.Service)
		}
	}
	log.Printf("pid cache: %d channels loaded", len(entries))
}

func savePIDCache() {
	path, err := pidCachePath()
	if err != nil {
		return
	}
	entries := map[string]pidCacheEntry{}
	pidExpandCache.Range(func(k, v any) bool {
		e := pidCacheEntry{Expanded: v.(string)}
		if s, ok := progNumCache.Load(k); ok {
			e.Service = s.(int)
		}
		entries[k.(string)] = e
		return true
	})
	data, err := json.MarshalIndent(entries, "", " ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, data, 0o644)
}

// channelService returns the channel's DVB service id once the PMT probe
// has resolved it.
func channelService(channelURL string) (int, bool) {
	if v, ok := progNumCache.Load(channelURL); ok {
		return v.(int), true
	}
	return 0, false
}

// expandChannelPIDs returns the channel URL with its pids parameter
// extended to all elementary streams found in the PMT. Results are cached;
// on any failure the original URL is returned so playback never blocks on
// this optimization.
func expandChannelPIDs(channelURL string) string {
	if !strings.HasPrefix(channelURL, "rtsp") || !strings.Contains(channelURL, "pids=") {
		return channelURL
	}
	if v, ok := pidExpandCache.Load(channelURL); ok {
		return v.(string)
	}
	pids, err := probeProgramPIDs(channelURL, 2500*time.Millisecond)
	if err != nil {
		log.Printf("pid expansion %s: %v", channelURL, err)
		return channelURL
	}
	expanded := mergePIDsIntoURL(channelURL, pids)
	pidExpandCache.Store(channelURL, expanded)
	savePIDCache()
	if expanded != channelURL {
		log.Printf("expanded pids for %s -> %s", channelURL, expanded[strings.Index(expanded, "pids="):])
	}
	return expanded
}

// cachedExpandedURL returns the expanded URL when already known, without
// ever blocking — the play path must stay snappy.
func cachedExpandedURL(channelURL string) string {
	if v, ok := pidExpandCache.Load(channelURL); ok {
		return v.(string)
	}
	return channelURL
}

var expandSem = make(chan struct{}, 1)

// prefetchPIDExpansions warms the expansion cache in the background, one
// short-lived session at a time.
func prefetchPIDExpansions(urls []string) {
	go func() {
		expandSem <- struct{}{}
		defer func() { <-expandSem }()
		for _, u := range urls {
			if _, ok := pidExpandCache.Load(u); ok {
				continue
			}
			expandChannelPIDs(u)
		}
	}()
}

// probeProgramPIDs opens a short-lived SAT>IP session and reads the
// channel's PAT and PMT to learn every elementary stream PID.
func probeProgramPIDs(channelURL string, timeout time.Duration) ([]int, error) {
	s, err := dialSatIP(channelURL)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	requested := urlPIDSet(channelURL)
	var patAsm, pmtAsm sectionAssembler
	pmtPID := -1
	off := s.ring.Start()
	buf := make([]byte, 64*1024)
	pending := []byte{}
	deadline := time.Now().Add(timeout)
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
			switch pid := tsPID(pkt); {
			case pid == 0 && pmtPID < 0:
				if sec := patAsm.feed(pkt); sec != nil {
					// The PAT lists every service on the mux; our channel's
					// PMT is the one the URL actually requests.
					for _, p := range parsePAT(sec) {
						if requested[p] {
							pmtPID = p
							break
						}
					}
				}
			case pid == pmtPID:
				if sec := pmtAsm.feed(pkt); sec != nil {
					if pids := parsePMTPIDs(sec); len(pids) > 0 {
						// Program number doubles as the EIT service id.
						progNumCache.Store(channelURL, int(sec[3])<<8|int(sec[4]))
						return pids, nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("no PAT/PMT within %v", timeout)
}
