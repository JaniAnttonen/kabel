package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"net/textproto"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// satipSession is one SAT>IP RTSP session streaming a channel's MPEG-TS
// into a ring buffer. Used by the prebuffer to keep adjacent channels warm
// so zapping is instant.
type satipSession struct {
	channelURL string
	ring       *tsRing

	mu      sync.Mutex // guards control-connection IO and cseq
	ctrl    net.Conn
	tp      *textproto.Reader
	cseq    int
	sessID  string
	control string // request URL for PLAY/OPTIONS/TEARDOWN

	rtp  *net.UDPConn
	rtcp *net.UDPConn
	// server RTP/RTCP addresses for NAT hole punching (nil when unknown)
	punchRTP  *net.UDPAddr
	punchRTCP *net.UDPAddr
	bytes     atomic.Int64 // TS bytes received (throughput diagnostics)
	psiOnly   bool         // low-rate tables-only session; skip starvation checks
	stop      chan struct{}
	once      sync.Once
}

// dialSatIP establishes a verified streaming session. NAT port rewriting
// can silently break any individual attempt (the box streams to the
// client_port it was told, which the router may not map back to us), so
// each attempt must prove data flow and gets fresh ports otherwise.
func dialSatIP(channelURL string) (*satipSession, error) {
	return dialSatIPBytes(channelURL, 64*1024)
}

// dialSatIPBytes verifies at least minBytes flow within the probe window —
// PSI-only sessions (EPG sweeps) trickle far slower than full TV streams.
func dialSatIPBytes(channelURL string, minBytes int64) (*satipSession, error) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var s *satipSession
		s, err = dialSatIPOnce(channelURL)
		if err != nil {
			continue
		}
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) && s.bytes.Load() < minBytes {
			time.Sleep(50 * time.Millisecond)
		}
		if s.bytes.Load() >= minBytes {
			return s, nil
		}
		s.Close()
		err = fmt.Errorf("session established but no data flowing (NAT?)")
	}
	return nil, err
}

func dialSatIPOnce(channelURL string) (*satipSession, error) {
	u, err := neturl.Parse(channelURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		host += ":554"
	}
	ctrl, err := net.DialTimeout("tcp", host, 4*time.Second)
	if err != nil {
		return nil, err
	}
	s := &satipSession{
		channelURL: channelURL,
		ring:       newTSRing(ringCap),
		ctrl:       ctrl,
		tp:         textproto.NewReader(bufio.NewReader(ctrl)),
		control:    channelURL,
		stop:       make(chan struct{}),
	}
	rtp, rtcp, err := listenRTPPair()
	if err != nil {
		ctrl.Close()
		return nil, err
	}
	s.rtp, s.rtcp = rtp, rtcp
	_ = rtp.SetReadBuffer(8 << 20) // SAT>IP HD bursts overflow small buffers

	port := rtp.LocalAddr().(*net.UDPAddr).Port
	hdrs, err := s.request("SETUP", s.control,
		fmt.Sprintf("Transport: RTP/AVP;unicast;client_port=%d-%d", port, port+1))
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("SETUP: %w", err)
	}
	s.sessID = strings.SplitN(hdrs.Get("Session"), ";", 2)[0]
	if id := hdrs.Get("com.ses.streamID"); id != "" {
		s.control = fmt.Sprintf("rtsp://%s/stream=%s", host, id)
	}
	// When the box is behind a router (routed LAN), its RTP can only reach
	// us through a NAT pinhole we open ourselves — same trick as ffmpeg's
	// ff_rtp_send_punch_packets, and like ffmpeg it must happen exactly
	// once, before PLAY: the box kills the stream if junk RTP arrives at
	// its media port mid-session. The pinhole stays warm afterwards via
	// valid RTCP receiver reports (and inbound traffic refreshing NAT
	// conntrack).
	if lo, hi, ok := transportServerPorts(hdrs.Get("Transport")); ok {
		boxIP := net.ParseIP(u.Hostname())
		if boxIP != nil {
			s.punchRTP = &net.UDPAddr{IP: boxIP, Port: lo}
			s.punchRTCP = &net.UDPAddr{IP: boxIP, Port: hi}
		}
	}
	s.punch()
	if _, err := s.request("PLAY", s.control, ""); err != nil {
		s.Close()
		return nil, fmt.Errorf("PLAY: %w", err)
	}
	go s.rtpLoop()
	go s.keepaliveLoop()
	return s, nil
}

// request performs one RTSP request/response on the control connection.
func (s *satipSession) request(method, url, extra string) (textproto.MIMEHeader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cseq++
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s RTSP/1.0\r\nCSeq: %d\r\nUser-Agent: kabel\r\n", method, url, s.cseq)
	if s.sessID != "" {
		fmt.Fprintf(&b, "Session: %s\r\n", s.sessID)
	}
	if extra != "" {
		b.WriteString(extra + "\r\n")
	}
	b.WriteString("\r\n")
	_ = s.ctrl.SetDeadline(time.Now().Add(4 * time.Second))
	defer s.ctrl.SetDeadline(time.Time{})
	if _, err := s.ctrl.Write([]byte(b.String())); err != nil {
		return nil, err
	}
	status, err := s.tp.ReadLine()
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(status)
	if len(fields) < 2 || fields[1] != "200" {
		return nil, fmt.Errorf("rtsp %s: %s", method, status)
	}
	// The Fritz!Box replies with quirky CSeq values; don't validate them.
	return s.tp.ReadMIMEHeader()
}

func (s *satipSession) rtpLoop() {
	buf := make([]byte, 2048)
	for {
		_ = s.rtp.SetReadDeadline(time.Now().Add(12 * time.Second))
		n, err := s.rtp.Read(buf)
		if err != nil {
			select {
			case <-s.stop:
			default:
				log.Printf("satip %s: rtp read: %v", s.channelURL, err)
				s.Close()
			}
			return
		}
		if p := rtpPayload(buf[:n]); len(p) > 0 {
			s.bytes.Add(int64(len(p)))
			s.ring.Write(p)
		}
	}
}

// punch sends dummy datagrams from our RTP/RTCP sockets towards the server
// so intermediate NATs map the return path. A minimal RTP header keeps the
// box from logging garbage.
func (s *satipSession) punch() {
	pkt := []byte{0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if s.punchRTP != nil {
		_, _ = s.rtp.WriteToUDP(pkt, s.punchRTP)
	}
	if s.punchRTCP != nil {
		_, _ = s.rtcp.WriteToUDP(pkt, s.punchRTCP)
	}
}

// transportServerPorts extracts server_port=lo-hi from an RTSP Transport
// header.
func transportServerPorts(transport string) (lo, hi int, ok bool) {
	for _, part := range strings.Split(transport, ";") {
		v, found := strings.CutPrefix(strings.TrimSpace(part), "server_port=")
		if !found {
			continue
		}
		lohi := strings.SplitN(v, "-", 2)
		lo, err := strconv.Atoi(strings.TrimSpace(lohi[0]))
		if err != nil {
			return 0, 0, false
		}
		hi = lo + 1
		if len(lohi) == 2 {
			if h, err := strconv.Atoi(strings.TrimSpace(lohi[1])); err == nil {
				hi = h
			}
		}
		return lo, hi, true
	}
	return 0, 0, false
}

// sendRR sends a minimal valid RTCP receiver report to the server's RTCP
// port: proper client behaviour that also refreshes the NAT mapping.
func (s *satipSession) sendRR() {
	if s.punchRTCP == nil {
		return
	}
	rr := []byte{0x80, 201, 0, 1, 0, 0, 0, 1} // V=2, RC=0, PT=RR, len=1, SSRC=1
	_, _ = s.rtcp.WriteToUDP(rr, s.punchRTCP)
}

// keepaliveLoop refreshes the RTSP session (SAT>IP servers expire sessions
// after ~60s without traffic), reports reception via RTCP, and logs
// throughput so starving sessions are visible.
func (s *satipSession) keepaliveLoop() {
	rtsp := time.NewTicker(25 * time.Second)
	rtcp := time.NewTicker(15 * time.Second)
	defer rtsp.Stop()
	defer rtcp.Stop()
	var lastBytes int64
	for {
		select {
		case <-s.stop:
			return
		case <-rtcp.C:
			s.sendRR()
			cur := s.bytes.Load()
			delta := cur - lastBytes
			lastBytes = cur
			if s.psiOnly {
				continue // PSI sessions legitimately trickle
			}
			if delta < 200*1024 {
				// Starving (healthy TV muxes deliver megabytes per tick):
				// close so the next ensure/zap redials with fresh ports.
				log.Printf("satip %s: starving (+%d KB), closing", s.channelURL, delta/1024)
				s.Close()
				return
			}
			log.Printf("satip %s: +%d KB buffered", s.channelURL, delta/1024)
		case <-rtsp.C:
			if _, err := s.request("OPTIONS", s.control, ""); err != nil {
				log.Printf("satip %s: keepalive: %v", s.channelURL, err)
				s.Close()
				return
			}
		}
	}
}

func (s *satipSession) closed() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *satipSession) Close() {
	s.once.Do(func() {
		close(s.stop)
		// Best-effort TEARDOWN so the box frees the tuner promptly.
		go func() {
			_, _ = s.request("TEARDOWN", s.control, "")
			s.ctrl.Close()
		}()
		time.AfterFunc(2*time.Second, func() { s.ctrl.Close() })
		s.rtp.Close()
		s.rtcp.Close()
		s.ring.Close()
	})
}

// listenRTPPair binds an even/odd UDP port pair as RTP requires.
func listenRTPPair() (rtp, rtcp *net.UDPConn, err error) {
	for i := 0; i < 40; i++ {
		c1, err := net.ListenUDP("udp4", &net.UDPAddr{})
		if err != nil {
			return nil, nil, err
		}
		port := c1.LocalAddr().(*net.UDPAddr).Port
		if port%2 != 0 {
			c1.Close()
			continue
		}
		c2, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port + 1})
		if err != nil {
			c1.Close()
			continue
		}
		return c1, c2, nil
	}
	return nil, nil, fmt.Errorf("no free RTP port pair")
}

// rtpPayload strips the RTP header (with CSRC and extension handling) and
// returns the MPEG-TS payload. Servers sending raw TS over UDP are handled
// too (packets starting with the TS sync byte).
func rtpPayload(b []byte) []byte {
	if len(b) > 0 && b[0] == 0x47 {
		return b // raw TS, no RTP framing
	}
	if len(b) < 12 || b[0]>>6 != 2 {
		return nil
	}
	h := 12 + 4*int(b[0]&0x0F)
	if b[0]&0x10 != 0 { // header extension
		if len(b) < h+4 {
			return nil
		}
		h += 4 + 4*int(binary.BigEndian.Uint16(b[h+2:h+4]))
	}
	if h >= len(b) {
		return nil
	}
	return b[h:]
}
