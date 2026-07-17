package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRTPPayload(t *testing.T) {
	ts := bytes.Repeat([]byte{0x47, 1, 2, 3}, 47) // 188 bytes starting with sync
	plain := append([]byte{0x80, 33, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1}, ts...)
	if got := rtpPayload(plain); !bytes.Equal(got, ts) {
		t.Errorf("plain RTP: payload mismatch (%d bytes)", len(got))
	}
	// One CSRC entry (CC=1) adds 4 bytes.
	withCSRC := append([]byte{0x81, 33, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 9, 9, 9, 9}, ts...)
	if got := rtpPayload(withCSRC); !bytes.Equal(got, ts) {
		t.Errorf("CSRC RTP: payload mismatch (%d bytes)", len(got))
	}
	// Extension header: 4-byte header declaring one 32-bit word.
	withExt := append([]byte{0x90, 33, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0xBE, 0xDE, 0, 1, 5, 5, 5, 5}, ts...)
	if got := rtpPayload(withExt); !bytes.Equal(got, ts) {
		t.Errorf("ext RTP: payload mismatch (%d bytes)", len(got))
	}
	// Raw TS without RTP framing passes through whole.
	if got := rtpPayload(ts); !bytes.Equal(got, ts) {
		t.Errorf("raw TS: payload mismatch")
	}
	if rtpPayload([]byte{0x80, 1}) != nil {
		t.Errorf("truncated packet should yield nil")
	}
}

func TestTSRingFollowAndTrim(t *testing.T) {
	r := newTSRing(188 * 4)
	pkt := func(b byte) []byte { return bytes.Repeat([]byte{b}, 188) }
	r.Write(pkt(1))
	r.Write(pkt(2))

	buf := make([]byte, 1024)
	n, off, ok := r.ReadFrom(r.Start(), buf)
	if !ok || n != 376 || buf[0] != 1 || buf[188] != 2 {
		t.Fatalf("initial read: n=%d ok=%v", n, ok)
	}

	// Overflow: 4 more packets push out the first two.
	for b := byte(3); b <= 6; b++ {
		r.Write(pkt(b))
	}
	if r.Start() != 376 {
		t.Fatalf("base = %d, want 376", r.Start())
	}
	//

	// A reader that fell behind jumps forward to the window start.
	n, _, ok = r.ReadFrom(0, buf)
	if !ok || buf[0] != 3 {
		t.Fatalf("behind reader: first byte %d, want 3", buf[0])
	}
	_ = n

	// Blocking read wakes on write.
	done := make(chan byte, 1)
	go func() {
		b := make([]byte, 188)
		_, _, ok := r.ReadFrom(off+188*4, b)
		if !ok {
			done <- 0
			return
		}
		done <- b[0]
	}()
	time.Sleep(50 * time.Millisecond)
	r.Write(pkt(7))
	select {
	case b := <-done:
		if b != 7 {
			t.Fatalf("woken reader got %d, want 7", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader never woke")
	}

	r.Close()
	if _, _, ok := r.ReadFrom(1<<40, buf); ok {
		t.Fatal("read after close+drain should report !ok")
	}
}

// fakeSatIPServer speaks just enough RTSP to serve one SETUP/PLAY session,
// then fires RTP packets at the negotiated client port.
func fakeSatIPServer(t *testing.T, payload []byte) (rtspAddr string, done chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done = make(chan string, 4)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		var clientPort int
		for {
			var lines []string
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					return
				}
				line = strings.TrimRight(line, "\r\n")
				if line == "" {
					break
				}
				lines = append(lines, line)
			}
			method := strings.Fields(lines[0])[0]
			done <- method
			for _, l := range lines {
				if v, ok := strings.CutPrefix(l, "Transport:"); ok {
					for _, part := range strings.Split(v, ";") {
						if pv, ok := strings.CutPrefix(strings.TrimSpace(part), "client_port="); ok {
							p := strings.SplitN(pv, "-", 2)[0]
							clientPort, _ = strconv.Atoi(p)
						}
					}
				}
			}
			switch method {
			case "SETUP":
				fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: 0\r\nSession: 12345;timeout=60\r\ncom.ses.streamID: 7\r\n\r\n")
			case "PLAY":
				fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: 0\r\n\r\n")
				go func() {
					dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: clientPort}
					c, err := net.DialUDP("udp4", nil, dst)
					if err != nil {
						return
					}
					defer c.Close()
					hdr := []byte{0x80, 33, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1}
					// Enough volume to pass the 64KB flow verification.
					for i := 0; i < 600; i++ {
						c.Write(append(append([]byte{}, hdr...), payload...))
						if i%100 == 0 {
							time.Sleep(5 * time.Millisecond)
						}
					}
				}()
			default: // OPTIONS/TEARDOWN
				fmt.Fprintf(conn, "RTSP/1.0 200 OK\r\nCSeq: 0\r\n\r\n")
			}
		}
	}()
	return ln.Addr().String(), done
}

func TestSatIPSessionLoopback(t *testing.T) {
	payload := bytes.Repeat([]byte{0x47, 0xAB}, 94) // 188 bytes
	addr, done := fakeSatIPServer(t, payload)
	s, err := dialSatIP("rtsp://" + addr + "/?freq=314&fake=1")
	if err != nil {
		t.Fatalf("dialSatIP: %v", err)
	}
	defer s.Close()

	if m := <-done; m != "SETUP" {
		t.Fatalf("first request %q, want SETUP", m)
	}
	if m := <-done; m != "PLAY" {
		t.Fatalf("second request %q, want PLAY", m)
	}
	if !strings.HasSuffix(s.control, "/stream=7") {
		t.Errorf("control URL %q missing streamID", s.control)
	}

	buf := make([]byte, 4096)
	deadline := time.Now().Add(3 * time.Second)
	var got []byte
	off := s.ring.Start()
	for len(got) < 188 && time.Now().Before(deadline) {
		n, newOff, ok := s.ring.ReadFrom(off, buf)
		if !ok {
			break
		}
		got = append(got, buf[:n]...)
		off = newOff
	}
	if len(got) < 188 || !bytes.Equal(got[:188], payload) {
		t.Fatalf("ring received %d bytes, payload mismatch", len(got))
	}
}
