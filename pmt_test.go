package main

import (
	"testing"
)

// buildSection wraps a PSI payload with a valid section length and dummy CRC.
func buildSection(tableID byte, body []byte) []byte {
	slen := len(body) + 4 // body + CRC
	sec := []byte{tableID, 0xB0 | byte(slen>>8), byte(slen)}
	sec = append(sec, body...)
	return append(sec, 0, 0, 0, 0) // CRC (not validated)
}

func patSection(pmtPIDs ...int) []byte {
	body := []byte{
		0x00, 0x01, // transport stream id
		0xC1, 0x00, 0x00, // version/current, section 0 of 0
		0x00, 0x00, 0xE0, 0x10, // program 0 -> network PID (skipped)
	}
	for i, pid := range pmtPIDs {
		body = append(body, 0x00, byte(i+1), byte(0xE0|pid>>8), byte(pid))
	}
	return buildSection(0x00, body)
}

func pmtSection(pcr int, es map[int]byte) []byte {
	body := []byte{
		0x00, 0x01, // program number
		0xC1, 0x00, 0x00,
		byte(0xE0 | pcr>>8), byte(pcr),
		0xF0, 0x00, // program info length 0
	}
	for pid, st := range es {
		body = append(body, st, byte(0xE0|pid>>8), byte(pid), 0xF0, 0x00)
	}
	return buildSection(0x02, body)
}

func TestParsePATAndPMT(t *testing.T) {
	got := parsePAT(patSection(6904, 6903, 6905))
	if len(got) != 3 || got[1] != 6903 {
		t.Fatalf("parsePAT = %v, want [6904 6903 6905]", got)
	}
	set := urlPIDSet("rtsp://h/?avm=1&pids=0,16,6903,310&x=1")
	if !set[6903] || !set[310] || set[6904] {
		t.Fatalf("urlPIDSet = %v", set)
	}
	pids := parsePMTPIDs(pmtSection(310, map[int]byte{310: 0x1B, 1127: 0x06, 5101: 0x06}))
	want := map[int]bool{310: true, 1127: true, 5101: true}
	found := map[int]bool{}
	for _, p := range pids {
		found[p] = true
	}
	for p := range want {
		if !found[p] {
			t.Errorf("pid %d missing from %v", p, pids)
		}
	}
}

func TestSectionAssemblerMultiPacket(t *testing.T) {
	// >184 bytes so the section genuinely spans two TS packets.
	es := map[int]byte{}
	for pid := 1000; pid < 1040; pid++ {
		es[pid] = 0x06
	}
	sec := pmtSection(310, es)
	if len(sec) <= 184 {
		t.Fatalf("test section too small to span packets: %d", len(sec))
	}
	// Split across two TS packets on pid 6903.
	mk := func(pusi bool, cc byte, payload []byte) []byte {
		pkt := make([]byte, 188)
		pkt[0] = 0x47
		pkt[1] = 0x1A // pid 6903 high bits
		if pusi {
			pkt[1] |= 0x40
		}
		pkt[2] = 0xF7
		pkt[3] = 0x10 | cc
		p := 4
		if pusi {
			pkt[p] = 0 // pointer_field
			p++
		}
		n := copy(pkt[p:], payload)
		for i := p + n; i < 188; i++ {
			pkt[i] = 0xFF
		}
		return pkt
	}
	first := 183 // packet 1 payload after the pointer field
	var asm sectionAssembler
	if got := asm.feed(mk(true, 0, sec[:first])); got != nil {
		t.Fatal("section complete too early")
	}
	got := asm.feed(mk(false, 1, sec[first:]))
	if got == nil {
		t.Fatal("section not completed")
	}
	pids := parsePMTPIDs(got)
	if len(pids) < 40 {
		t.Fatalf("parsed %d pids from reassembled section, want >= 40", len(pids))
	}
}

func TestMergePIDsIntoURL(t *testing.T) {
	u := "rtsp://192.168.178.1:554/?avm=1&freq=314&pids=0,16,17,310"
	got := mergePIDsIntoURL(u, []int{1127, 310, 853})
	want := "rtsp://192.168.178.1:554/?avm=1&freq=314&pids=0,16,17,310,853,1127"
	if got != want {
		t.Errorf("merge = %q, want %q", got, want)
	}
	// pids mid-URL keeps trailing params.
	u2 := "rtsp://h/?pids=0,16&x=1"
	if got := mergePIDsIntoURL(u2, []int{20}); got != "rtsp://h/?pids=0,16,20&x=1" {
		t.Errorf("merge tail = %q", got)
	}
}
