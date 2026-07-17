package main

import (
	"testing"
	"time"
)

func TestNormalizeChannelName(t *testing.T) {
	cases := map[string]string{
		"Yle TV1 HD":         "yletv1",
		"YLE TV1":            "yletv1",
		"Yle Teema & Fem HD": "yleteemafem",
		"Yle Teema Fem":      "yleteemafem",
		"MTV3 HD":            "mtv3",
		"MTV AVA":            "mtvava",
		"STAR Channel":       "starchannel",
	}
	for in, want := range cases {
		if got := normalizeChannelName(in); got != want {
			t.Errorf("normalizeChannelName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseXMLTVTime(t *testing.T) {
	got := parseXMLTVTime("20260717032500 +0000")
	want := time.Date(2026, 7, 17, 3, 25, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseXMLTVTime = %v, want %v", got, want)
	}
	if !parseXMLTVTime("garbage").IsZero() {
		t.Error("expected zero time for garbage")
	}
}

func TestParseXMLTVAndMatch(t *testing.T) {
	// Programme times relative to now so the now/next logic is exercised.
	now := time.Now().UTC()
	fmtT := func(d time.Duration) string { return now.Add(d).Format("20060102150405 -0700") }
	doc := `<?xml version="1.0"?><tv>
<channel id="YLE.TV1.fi"><display-name>YLE TV1</display-name></channel>
<channel id="MTV3.fi"><display-name>MTV3</display-name></channel>
<programme start="` + fmtT(-30*time.Minute) + `" stop="` + fmtT(30*time.Minute) + `" channel="YLE.TV1.fi"><title>Ylen aamu</title><desc>Morning show</desc></programme>
<programme start="` + fmtT(30*time.Minute) + `" stop="` + fmtT(90*time.Minute) + `" channel="YLE.TV1.fi"><title>Uutiset</title></programme>
</tv>`
	if err := parseXMLTV([]byte(doc)); err != nil {
		t.Fatalf("parseXMLTV: %v", err)
	}
	// kabel's channel name should match the XMLTV display-name.
	nowE, nextE := xmltvNowNext("Yle TV1 HD")
	if nowE == nil || nowE.Title != "Ylen aamu" {
		t.Fatalf("now = %+v, want Ylen aamu", nowE)
	}
	if nowE.Text != "Morning show" {
		t.Errorf("desc = %q", nowE.Text)
	}
	if nextE == nil || nextE.Title != "Uutiset" {
		t.Fatalf("next = %+v, want Uutiset", nextE)
	}
	if n, _ := xmltvNowNext("Nonexistent"); n != nil {
		t.Error("expected no match for unknown channel")
	}
}
