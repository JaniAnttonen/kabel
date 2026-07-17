package main

import (
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"net/http"
	"sync"
)

const ringCap = 4 << 20 // ~2.5s of an HD mux; also the max distance behind live

// tsRing is a bounded byte window over a live MPEG-TS stream. Writers append
// (trimming the oldest 188-byte-aligned packets on overflow); readers follow
// by absolute offset and jump forward if they fall out of the window.
type tsRing struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	base   int64 // absolute stream offset of buf[0]
	max    int
	closed bool
}

func newTSRing(max int) *tsRing {
	r := &tsRing{max: max}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *tsRing) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - r.max; over > 0 {
		trim := (over + 187) / 188 * 188 // keep packet alignment
		if trim > len(r.buf) {
			trim = len(r.buf)
		}
		r.buf = append(r.buf[:0], r.buf[trim:]...)
		r.base += int64(trim)
	}
	r.cond.Broadcast()
}

// Start returns the oldest available absolute offset.
func (r *tsRing) Start() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.base
}

// ReadFrom blocks until data past off is available and copies it into p.
// It returns the new offset to continue from; io semantics end with ok=false
// once the ring is closed and drained.
func (r *tsRing) ReadFrom(off int64, p []byte) (n int, newOff int64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if off < r.base {
			off = r.base // fell behind the window; skip forward
		}
		if avail := r.base + int64(len(r.buf)) - off; avail > 0 {
			start := int(off - r.base)
			n = copy(p, r.buf[start:])
			return n, off + int64(n), true
		}
		if r.closed {
			return 0, off, false
		}
		r.cond.Wait()
	}
}

func (r *tsRing) Close() {
	r.mu.Lock()
	r.closed = true
	r.cond.Broadcast()
	r.mu.Unlock()
}

// Prebuffer keeps SAT>IP sessions warm for the current channel and its
// neighbors and serves their TS to mpv over a loopback HTTP proxy, so
// zapping starts from already-buffered data instead of a cold tuner.
type Prebuffer struct {
	mu       sync.Mutex
	sessions map[string]*satipSession // by channel RTSP URL
	port     int
	srv      *http.Server
}

func newPrebuffer() (*Prebuffer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Prebuffer{sessions: map[string]*satipSession{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/stream/", p.handleStream)
	p.srv = &http.Server{Handler: mux}
	p.port = ln.Addr().(*net.TCPAddr).Port
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

func streamID(channelURL string) string {
	h := fnv.New64a()
	h.Write([]byte(channelURL))
	return fmt.Sprintf("%x", h.Sum64())
}

// StreamURL ensures a session for the channel and returns the proxy URL for
// mpv to play. Falls back to the direct RTSP URL if the session can't start
// (e.g. all box tuners busy).
func (p *Prebuffer) StreamURL(channelURL string) string {
	if p.ensure(channelURL) == nil {
		return channelURL
	}
	return fmt.Sprintf("http://127.0.0.1:%d/stream/%s", p.port, streamID(channelURL))
}

func (p *Prebuffer) ensure(channelURL string) *satipSession {
	p.mu.Lock()
	if s, ok := p.sessions[channelURL]; ok && !s.closed() {
		p.mu.Unlock()
		return s
	}
	delete(p.sessions, channelURL)
	p.mu.Unlock()

	// Sessions are keyed by the raw channel URL but stream the pid-expanded
	// variant so subtitle/audio tracks are included.
	s, err := dialSatIP(cachedExpandedURL(channelURL)) // outside the lock; network IO
	if err != nil {
		log.Printf("prebuffer %s: %v", channelURL, err)
		return nil
	}
	log.Printf("prebuffer: session up for %s", channelURL)
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.sessions[channelURL]; ok && !old.closed() {
		go s.Close() // raced with another ensure; keep the first
		return old
	}
	p.sessions[channelURL] = s
	return s
}

// SetActive declares the currently playing channel and its zap neighbors.
// Sessions outside that set are torn down (freeing box tuners); neighbor
// sessions are started in the background. Empty current tears down all.
func (p *Prebuffer) SetActive(current string, neighbors []string) {
	keep := map[string]bool{}
	if current != "" {
		keep[current] = true
	}
	for _, u := range neighbors {
		keep[u] = true
	}
	p.mu.Lock()
	var drop []*satipSession
	for u, s := range p.sessions {
		if !keep[u] {
			drop = append(drop, s)
			delete(p.sessions, u)
		}
	}
	p.mu.Unlock()
	for _, s := range drop {
		log.Printf("prebuffer: dropping session %s", s.channelURL)
		go s.Close()
	}
	for _, u := range neighbors {
		go p.ensure(u)
	}
}

func (p *Prebuffer) handleStream(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Path[len("/stream/"):]
	p.mu.Lock()
	var sess *satipSession
	for u, s := range p.sessions {
		if streamID(u) == id && !s.closed() {
			sess = s
			break
		}
	}
	p.mu.Unlock()
	if sess == nil {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64*1024)
	off := sess.ring.Start() // serve the whole backlog: instant first frame
	for {
		n, newOff, ok := sess.ring.ReadFrom(off, buf)
		if !ok {
			return
		}
		off = newOff
		if _, err := w.Write(buf[:n]); err != nil {
			return // player went away
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (p *Prebuffer) Close() {
	p.SetActive("", nil)
	_ = p.srv.Close()
}
