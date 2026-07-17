package main

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/gen2brain/go-mpv"
	"github.com/go-gl/glfw/v3.3/glfw"
)

// ASS coordinate space for the OSD overlay; mpv scales it to the window.
const (
	osdResX    = 1280
	osdResY    = 720
	panelWidth = 500
	maxRows    = 20 // rows fit between header+status and footer at 28px each

	listOverlayID   int64 = 1
	volumeOverlayID int64 = 2
)

// errorColor is the only accent in the UI (ASS BGR for system red #FF453A);
// everything else is monochrome per the HIG's guidance for media apps.
const errorColor = "&H3A45FF&"

type UI struct {
	m   *mpv.Mpv
	win *glfw.Window
	zap *Prebuffer // adjacent-channel prebuffering; nil disables

	localChans  []Channel // discovered Fritz!Box channels, pinned on top
	publicChans []Channel
	channels    []Channel  // merged display order (arrangeChannels)
	items       []listItem // grouped rows incl. section headers
	home        string     // user country for relevance ordering
	loadErr     error

	filter   []rune
	view     []listItem // rows currently shown: items, or flat search hits
	matchBuf []listItem
	sel      int // selection within view (always a channel row)
	scroll   int // first visible row within view

	visible   bool
	current   int // index into channels currently playing, -1 = none
	status    string
	statusErr bool // status describes a failure (rendered in red)

	retried    bool   // current play attempt is already a retry
	lastMpvErr string // most recent error-level mpv log line

	fullscreen             bool
	winX, winY, winW, winH int

	scrollAccum float64 // fractional scroll-wheel ticks not yet applied

	panelPos *anim // panel slide: 0 = onscreen, -1 = offscreen
	pillY    *anim // selection pill glide between rows

	volShown     bool
	volArc       *anim // displayed ring value sweep
	volAlpha     *anim // ring fade in/out (0..1)
	volDisplayed float64
	volMuted     bool
	volHideAt    time.Time
}

// anim is a single eased transition between two values.
type anim struct {
	from, to float64
	start    time.Time
	dur      time.Duration
}

func newAnim(from, to float64, dur time.Duration) *anim {
	return &anim{from: from, to: to, start: time.Now(), dur: dur}
}

// at returns the eased (cubic ease-out) value at the given time.
func (a *anim) at(now time.Time) float64 {
	if a == nil {
		return 0
	}
	t := now.Sub(a.start).Seconds() / a.dur.Seconds()
	if t >= 1 {
		return a.to
	}
	if t < 0 {
		t = 0
	}
	p := 1 - math.Pow(1-t, 3)
	return a.from + (a.to-a.from)*p
}

func (a *anim) done(now time.Time) bool {
	return a == nil || now.Sub(a.start) >= a.dur
}

func newUI(m *mpv.Mpv, win *glfw.Window, public []Channel, loadErr error) *UI {
	ui := &UI{m: m, win: win, publicChans: public, loadErr: loadErr, current: -1, home: homeCountry()}
	log.Printf("home country for channel ordering: %q", ui.home)
	ui.rebuild()
	return ui
}

// rebuild recomputes the merged channel order and display rows after a
// source changed. The currently playing channel keeps playing; its index is
// re-resolved by URL so the ● marker stays correct.
func (ui *UI) rebuild() {
	var currentURL string
	if ui.current >= 0 && ui.current < len(ui.channels) {
		currentURL = ui.channels[ui.current].URL
	}
	ui.channels, ui.items = arrangeChannels(ui.localChans, ui.publicChans, ui.home)
	ui.current = -1
	for i, c := range ui.channels {
		if currentURL != "" && c.URL == currentURL {
			ui.current = i
			break
		}
	}
	ui.applyFilter()

	// Warm the pid-expansion cache for local channels so subtitle/audio
	// tracks are complete by the time they're tuned.
	var rtspURLs []string
	for _, c := range ui.channels {
		if strings.HasPrefix(c.URL, "rtsp") {
			rtspURLs = append(rtspURLs, c.URL)
		}
	}
	if len(rtspURLs) > 0 {
		prefetchPIDExpansions(rtspURLs)
	}
}

// setPublic swaps in a freshly fetched public channel list.
func (ui *UI) setPublic(channels []Channel) {
	ui.publicChans = channels
	ui.loadErr = nil
	ui.refresh(fmt.Sprintf("Channel list updated (%d channels)", len(ui.localChans)+len(channels)))
}

// setLocal swaps in (or clears) the discovered Fritz!Box channels.
func (ui *UI) setLocal(channels []Channel) {
	had := len(ui.localChans) > 0
	ui.localChans = channels
	msg := ""
	switch {
	case len(channels) > 0:
		msg = fmt.Sprintf("Fritz!Box found — %d local channels", len(channels))
	case had:
		msg = "Fritz!Box no longer reachable"
	}
	ui.refresh(msg)
}

func (ui *UI) refresh(msg string) {
	ui.rebuild()
	log.Printf("channel list: %d local + %d public", len(ui.localChans), len(ui.publicChans))
	if ui.visible {
		ui.render()
	} else if msg != "" {
		ui.osdMsg(msg)
	}
}

func (ui *UI) showList() {
	if !ui.visible {
		start := -1.0
		if ui.panelPos != nil { // reverse a hide mid-flight
			start = ui.panelPos.at(time.Now())
		}
		ui.panelPos = newAnim(start, 0, 240*time.Millisecond)
	}
	ui.visible = true
	ui.render()
}

func (ui *UI) hideList() {
	if ui.visible {
		start := 0.0
		if ui.panelPos != nil {
			start = ui.panelPos.at(time.Now())
		}
		ui.panelPos = newAnim(start, -1, 200*time.Millisecond)
	}
	ui.visible = false
	ui.render()
}

// tick advances animations and re-renders the affected overlays. It returns
// true while anything is still moving so the event loop can tighten its
// timeout for smooth frames.
func (ui *UI) tick() bool {
	now := time.Now()
	active := false

	if ui.panelPos != nil || ui.pillY != nil {
		if ui.panelPos.done(now) {
			ui.panelPos = nil
		}
		if ui.pillY.done(now) {
			ui.pillY = nil
		}
		ui.render()
		active = ui.panelPos != nil || ui.pillY != nil
	}

	if ui.volShown {
		fading := ui.volAlpha != nil && ui.volAlpha.to == 0
		switch {
		case !ui.volArc.done(now) || (ui.volAlpha != nil && !ui.volAlpha.done(now)):
			ui.renderVolume(now)
			active = true
		case fading:
			ui.setOverlayID(volumeOverlayID, "none", "")
			ui.volShown = false
			ui.volArc, ui.volAlpha = nil, nil
		case now.After(ui.volHideAt):
			ui.volAlpha = newAnim(1, 0, 220*time.Millisecond)
			active = true
		}
	}
	return active
}

func (ui *UI) applyFilter() {
	needle := strings.ToLower(string(ui.filter))
	if needle == "" {
		ui.view = ui.items
	} else {
		// Flat result rows while searching; headers only in browse mode.
		ui.matchBuf = ui.matchBuf[:0]
		for i, c := range ui.channels {
			if strings.Contains(strings.ToLower(c.Name), needle) {
				ui.matchBuf = append(ui.matchBuf, listItem{chanIdx: i})
			}
		}
		ui.view = ui.matchBuf
	}
	if ui.sel >= len(ui.view) {
		ui.sel = len(ui.view) - 1
	}
	if ui.sel < 0 {
		ui.sel = 0
	}
	ui.sel = ui.snapSelectable(ui.sel, 1)
	ui.clampScroll()
	ui.pillY = nil // layout changed; don't glide across a reshuffle
}

// snapSelectable returns the nearest channel row to i, searching in dir
// first and backwards as fallback (headers are not selectable).
func (ui *UI) snapSelectable(i, dir int) int {
	if len(ui.view) == 0 {
		return 0
	}
	if i < 0 {
		i = 0
	}
	if i >= len(ui.view) {
		i = len(ui.view) - 1
	}
	for j := i; j >= 0 && j < len(ui.view); j += dir {
		if ui.view[j].header == "" {
			return j
		}
	}
	for j := i - dir; j >= 0 && j < len(ui.view); j -= dir {
		if ui.view[j].header == "" {
			return j
		}
	}
	return i
}

func (ui *UI) clampScroll() {
	if ui.sel < ui.scroll {
		ui.scroll = ui.sel
	}
	if ui.sel >= ui.scroll+maxRows {
		ui.scroll = ui.sel - maxRows + 1
	}
	// Reveal a section header sitting directly above the selection.
	if ui.sel == ui.scroll && ui.sel > 0 && ui.sel-1 < len(ui.view) && ui.view[ui.sel-1].header != "" {
		ui.scroll = ui.sel - 1
	}
	maxScroll := len(ui.view) - maxRows
	if maxScroll < 0 {
		maxScroll = 0
	}
	if ui.scroll > maxScroll {
		ui.scroll = maxScroll
	}
	if ui.scroll < 0 {
		ui.scroll = 0
	}
}

func (ui *UI) moveSel(delta int) {
	if len(ui.view) == 0 {
		return
	}
	oldY := ui.pillTargetY()
	if ui.pillY != nil {
		oldY = ui.pillY.at(time.Now())
	}
	oldScroll := ui.scroll
	dir := 1
	if delta < 0 {
		dir = -1
	}
	ui.sel = ui.snapSelectable(ui.sel+delta, dir)
	ui.clampScroll()
	if ui.scroll == oldScroll {
		// Same viewport: glide the pill; on scroll jumps it snaps.
		ui.pillY = newAnim(oldY, ui.pillTargetY(), 130*time.Millisecond)
	} else {
		ui.pillY = nil
	}
	ui.render()
}

// pillTargetY is the resting y of the selection pill in the current layout.
func (ui *UI) pillTargetY() float64 {
	firstRowLine := 3
	if ui.status != "" {
		firstRowLine++
	}
	return float64(lineY(firstRowLine + ui.sel - ui.scroll))
}

func (ui *UI) play(idx int) {
	ui.playAttempt(idx, false)
}

func (ui *UI) playAttempt(idx int, isRetry bool) {
	if idx < 0 || idx >= len(ui.channels) {
		return
	}
	c := ui.channels[idx]
	target := c.URL
	if strings.HasPrefix(c.URL, "rtsp") {
		// Pin RTP over UDP: the Fritz!Box rejects TCP interleaving (461)
		// and ffmpeg's auto-negotiation trips over its RTSP quirks. SAT>IP
		// HD bursts overflow the OS default UDP receive buffer — showing
		// as RTP loss and heavy macroblocking — hence the big buffer.
		if err := ui.m.SetOptionString("rtsp-transport", "udp"); err != nil {
			log.Printf("rtsp-transport: %v", err)
		}
		// Small probe window: cuts time-to-first-frame from ~5.5s to ~1.8s
		// (PAT/PMT arrive within tens of ms on SAT>IP).
		if err := ui.m.SetOptionString("demuxer-lavf-o",
			"buffer_size=8388608,probesize=600000,analyzeduration=700000"); err != nil {
			log.Printf("rtsp buffer option: %v", err)
		}
		// Use the pid-expanded URL (all subtitle/audio streams) once the
		// background PMT probe has resolved it.
		target = cachedExpandedURL(c.URL)
		// Play through the prebuffer proxy: a warm neighbor session starts
		// from its buffer instantly. Retries bypass it to isolate faults.
		if ui.zap != nil && !isRetry {
			target = ui.zap.StreamURL(c.URL)
		}
	} else {
		_ = ui.m.SetOptionString("demuxer-lavf-o", "")
		if ui.zap != nil {
			ui.zap.SetActive("", nil) // free the box tuners
		}
	}
	ui.lastMpvErr = ""
	ui.retried = isRetry
	if err := ui.m.Command([]string{"loadfile", target}); err != nil {
		ui.status = "Failed to start: " + c.Name
		ui.statusErr = true
		log.Printf("loadfile %s: %v", c.URL, err)
		ui.render()
		return
	}
	ui.current = idx
	ui.statusErr = false
	if isRetry {
		ui.status = "Retrying " + c.Name + "…"
	} else {
		ui.status = "Tuning " + c.Name + "…"
	}
	ui.win.SetTitle(c.Name + " — kabel")
	if ui.zap != nil && strings.HasPrefix(c.URL, "rtsp") {
		ui.zap.SetActive(c.URL, ui.neighborRTSPURLs(idx))
	}
	ui.hideList()
}

// neighborRTSPURLs returns the zapper neighbors (±1 with wraparound, like
// step) that are local RTSP channels and thus worth prebuffering.
func (ui *UI) neighborRTSPURLs(idx int) []string {
	n := len(ui.channels)
	if n < 2 {
		return nil
	}
	var urls []string
	for _, d := range []int{-1, 1} {
		u := ui.channels[(idx+d+n)%n].URL
		if strings.HasPrefix(u, "rtsp") && u != ui.channels[idx].URL {
			urls = append(urls, u)
		}
	}
	return urls
}

// step switches to the previous/next channel relative to the current one
// (zapper behaviour while the list is hidden).
func (ui *UI) step(delta int) {
	if len(ui.channels) == 0 {
		return
	}
	idx := ui.current + delta
	if idx < 0 {
		idx = len(ui.channels) - 1
	}
	if idx >= len(ui.channels) {
		idx = 0
	}
	ui.play(idx)
}

func (ui *UI) playbackStarted() {
	if ui.current >= 0 {
		ui.status = ""
		ui.statusErr = false
		ui.render()
	}
}

func (ui *UI) playbackEnded(ef mpv.EventEndFile) {
	if ui.current < 0 {
		return
	}
	// "stop" fires when loadfile replaces the running stream (channel zap,
	// idle source swap) and "quit" on shutdown — neither is a failure.
	if ef.Reason == mpv.EndFileStop || ef.Reason == mpv.EndFileQuit {
		return
	}
	c := ui.channels[ui.current]
	log.Printf("playback ended: %s reason=%s err=%v mpv=%q", c.Name, ef.Reason, ef.Error, ui.lastMpvErr)

	// A momentarily busy tuner or a dropped RTSP setup is transient; give
	// local streams one automatic retry before surfacing the failure.
	if !ui.retried && strings.HasPrefix(c.URL, "rtsp") {
		ui.playAttempt(ui.current, true)
		return
	}

	msg := "Playback failed: " + c.Name
	if ef.Reason == mpv.EndFileEOF {
		msg = "Stream ended: " + c.Name
	}
	// The first captured log line names the root cause (e.g. "connection
	// refused"); the end-file error is often just "unrecognized file format".
	if ui.lastMpvErr != "" {
		msg += " — " + ui.lastMpvErr
	} else if ef.Error != nil {
		msg += " — " + ef.Error.Error()
	}
	ui.status = msg
	ui.statusErr = true
	ui.current = -1
	ui.win.SetTitle("kabel")
	if err := ui.m.Command([]string{"loadfile", idleSource}); err != nil {
		log.Printf("idle source: %v", err)
	}
	ui.showList()
}

// noteMpvLog remembers the first error-level mpv log line of the current
// play attempt (the root cause; later errors are usually generic fallout).
func (ui *UI) noteMpvLog(lm mpv.EventLogMessage) {
	if lm.Level != "error" && lm.Level != "fatal" {
		return
	}
	// Renderer/output noise (hwdec probing etc.) isn't a stream failure.
	p := lm.Prefix
	if strings.Contains(p, "render") || strings.Contains(p, "videotoolbox") ||
		strings.HasPrefix(p, "vo") || strings.HasPrefix(p, "ao") {
		return
	}
	if ui.lastMpvErr == "" {
		ui.lastMpvErr = strings.TrimSpace(p + ": " + lm.Text)
	}
}

func (ui *UI) handleChar(r rune) {
	if !ui.visible || r < 0x20 {
		return
	}
	ui.filter = append(ui.filter, r)
	ui.sel = 0
	ui.scroll = 0
	ui.applyFilter()
	ui.render()
}

func (ui *UI) handleKey(key glfw.Key, mods glfw.ModifierKey) {
	if mods&glfw.ModSuper != 0 {
		switch key {
		case glfw.KeyQ, glfw.KeyW:
			ui.win.SetShouldClose(true)
		}
		return
	}

	if ui.visible {
		switch key {
		case glfw.KeyUp:
			ui.moveSel(-1)
		case glfw.KeyDown:
			ui.moveSel(1)
		case glfw.KeyPageUp:
			ui.moveSel(-maxRows)
		case glfw.KeyPageDown:
			ui.moveSel(maxRows)
		case glfw.KeyEnter, glfw.KeyKPEnter:
			if ui.sel < len(ui.view) && ui.view[ui.sel].header == "" {
				ui.play(ui.view[ui.sel].chanIdx)
			}
		case glfw.KeyBackspace:
			if len(ui.filter) > 0 {
				ui.filter = ui.filter[:len(ui.filter)-1]
				ui.applyFilter()
				ui.render()
			}
		case glfw.KeyEscape:
			if len(ui.filter) > 0 {
				ui.filter = ui.filter[:0]
				ui.applyFilter()
				ui.render()
			} else if ui.current >= 0 {
				ui.hideList()
			}
		case glfw.KeyTab:
			ui.hideList()
		}
		return
	}

	switch key {
	case glfw.KeyTab, glfw.KeyEnter, glfw.KeyKPEnter, glfw.KeyEscape:
		ui.showList()
	case glfw.KeyUp:
		ui.addVolume(5)
	case glfw.KeyDown:
		ui.addVolume(-5)
	case glfw.KeyLeft:
		ui.step(-1)
	case glfw.KeyRight:
		ui.step(1)
	case glfw.KeyM:
		ui.command("cycle", "mute")
		ui.showVolume()
	case glfw.KeyS:
		ui.command("cycle", "sub")
		ui.showTrackOSD("sub", "Subtitles")
	case glfw.KeyA:
		ui.command("cycle", "audio")
		ui.showTrackOSD("audio", "Audio")
	case glfw.KeySpace:
		ui.command("cycle", "pause")
	case glfw.KeyF:
		ui.toggleFullscreen()
	case glfw.KeyQ:
		ui.win.SetShouldClose(true)
	}
}

func (ui *UI) addVolume(delta float64) {
	ui.command("add", "volume", fmt.Sprintf("%.1f", delta))
	ui.showVolume()
}

// showVolume animates a circular volume indicator (progress ring with the
// percentage centered) in the lower-right corner: fade in, sweep the arc to
// the new value, fade out after a pause. tick() drives the frames.
func (ui *UI) showVolume() {
	vol, err := ui.m.GetProperty("volume", mpv.FormatDouble)
	if err != nil {
		return
	}
	v := vol.(float64)
	muted := false
	if mu, err := ui.m.GetProperty("mute", mpv.FormatFlag); err == nil {
		muted, _ = mu.(bool)
	}
	now := time.Now()
	cur := 0.0 // first appearance sweeps up from zero
	if ui.volShown {
		cur = ui.volArc.at(now)
		if ui.volAlpha != nil && ui.volAlpha.to == 0 { // reverse a fade-out
			ui.volAlpha = newAnim(ui.volAlpha.at(now), 1, 120*time.Millisecond)
		}
	} else {
		ui.volAlpha = newAnim(0, 1, 120*time.Millisecond)
	}
	ui.volArc = newAnim(cur, v, 180*time.Millisecond)
	ui.volDisplayed = v
	ui.volMuted = muted
	ui.volShown = true
	ui.volHideAt = now.Add(1400 * time.Millisecond)
	ui.renderVolume(now)
}

func (ui *UI) renderVolume(now time.Time) {
	alpha := 1.0
	if ui.volAlpha != nil {
		alpha = ui.volAlpha.at(now)
	}
	ui.setOverlayID(volumeOverlayID, "ass-events", volumeASS(ui.volArc.at(now), ui.volMuted, alpha))
}

// volumeASS renders the indicator: backdrop disc, faint full ring, progress
// arc from 12 o'clock, and the percentage (or mute mark) in the middle.
// alpha (0..1) scales every element for the fade in/out.
func volumeASS(v float64, muted bool, alpha float64) string {
	const cx, cy = float64(osdResX - 100), float64(osdResY - 100)
	const rOut, rIn = 46.0, 37.0
	var b strings.Builder
	fmt.Fprintf(&b, "{\\an7\\pos(0,0)\\bord0\\shad0\\1c&H101010&%s\\p1}%s{\\p0}\n",
		alphaTag(0x38, alpha), circlePath(cx, cy, 58))
	fmt.Fprintf(&b, "{\\an7\\pos(0,0)\\bord0\\shad0\\1c&H666666&%s\\p1}%s{\\p0}\n",
		alphaTag(0x90, alpha), ringPath(cx, cy, rOut, rIn, 0, 360))
	color := "&HFFFFFF&"
	label := fmt.Sprintf("%.0f%%", v)
	if muted {
		color = "&H888888&"
		label = "✕"
	}
	if v > 0 {
		fmt.Fprintf(&b, "{\\an7\\pos(0,0)\\bord0\\shad0\\1c%s%s\\p1}%s{\\p0}\n",
			color, alphaTag(0, alpha), ringPath(cx, cy, rOut, rIn, -90, -90+3.6*v))
	}
	fmt.Fprintf(&b, "{\\an5\\pos(%.0f,%.0f)\\bord0\\shad0\\fs24\\b1\\1c&HFFFFFF&%s}%s\n",
		cx, cy, alphaTag(0, alpha), label)
	return b.String()
}

// alphaTag combines an element's base ASS transparency (0 = opaque,
// 255 = invisible) with an animation visibility factor (1 = fully shown).
func alphaTag(base int, visible float64) string {
	if visible > 1 {
		visible = 1
	}
	if visible < 0 {
		visible = 0
	}
	a := 255 - int(float64(255-base)*visible)
	return fmt.Sprintf("\\1a&H%02X&", a)
}

// arcSegs approximates a circular arc from a0 to a1 (degrees) with cubic
// béziers, one per quarter turn, as ASS drawing segments continuing from
// the arc's start point.
func arcSegs(cx, cy, r, a0deg, a1deg float64) string {
	a0 := a0deg * math.Pi / 180
	a1 := a1deg * math.Pi / 180
	n := int(math.Ceil(math.Abs(a1-a0) / (math.Pi / 2)))
	if n < 1 {
		n = 1
	}
	step := (a1 - a0) / float64(n)
	var sb strings.Builder
	for i := 0; i < n; i++ {
		s := a0 + float64(i)*step
		e := s + step
		k := 4.0 / 3.0 * math.Tan((e-s)/4)
		x0, y0 := cx+r*math.Cos(s), cy+r*math.Sin(s)
		x3, y3 := cx+r*math.Cos(e), cy+r*math.Sin(e)
		fmt.Fprintf(&sb, "b %.1f %.1f %.1f %.1f %.1f %.1f ",
			x0-k*r*math.Sin(s), y0+k*r*math.Cos(s),
			x3+k*r*math.Sin(e), y3-k*r*math.Cos(e),
			x3, y3)
	}
	return sb.String()
}

func circlePath(cx, cy, r float64) string {
	return fmt.Sprintf("m %.1f %.1f %s", cx+r, cy, arcSegs(cx, cy, r, 0, 360))
}

// ringPath draws an annulus (full circle) or annular sector between the
// outer and inner radii; the inner arc runs backwards so the fill winds
// correctly and leaves the hole.
func ringPath(cx, cy, rOut, rIn, a0, a1 float64) string {
	if a1-a0 >= 360 {
		return fmt.Sprintf("m %.1f %.1f %sm %.1f %.1f %s",
			cx+rOut, cy, arcSegs(cx, cy, rOut, 0, 360),
			cx+rIn, cy, arcSegs(cx, cy, rIn, 360, 0))
	}
	rad0, rad1 := a0*math.Pi/180, a1*math.Pi/180
	return fmt.Sprintf("m %.1f %.1f %sl %.1f %.1f %s",
		cx+rOut*math.Cos(rad0), cy+rOut*math.Sin(rad0),
		arcSegs(cx, cy, rOut, a0, a1),
		cx+rIn*math.Cos(rad1), cy+rIn*math.Sin(rad1),
		arcSegs(cx, cy, rIn, a1, a0))
}

// handleScroll navigates the channel list while it is open and controls the
// volume while watching. yoff is in scroll-wheel ticks (fractional on
// trackpads), positive = scroll up.
func (ui *UI) handleScroll(yoff float64) {
	if ui.visible {
		ui.scrollAccum += yoff
		steps := int(ui.scrollAccum)
		if steps != 0 {
			ui.scrollAccum -= float64(steps)
			ui.moveSel(-steps)
		}
		return
	}
	if ui.current >= 0 {
		ui.addVolume(2 * yoff)
	}
}

func (ui *UI) command(args ...string) {
	if err := ui.m.Command(args); err != nil {
		log.Printf("command %v: %v", args, err)
	}
}

// showTrackOSD reports the selected track after cycling (property expansion
// does not work through the client API, so read and format it ourselves).
func (ui *UI) showTrackOSD(kind, label string) {
	lang, err := ui.m.GetProperty("current-tracks/"+kind+"/lang", mpv.FormatString)
	if err != nil {
		ui.osdMsg(label + ": off")
		return
	}
	desc := fmt.Sprintf("%v", lang)
	if title, err := ui.m.GetProperty("current-tracks/"+kind+"/title", mpv.FormatString); err == nil {
		desc += fmt.Sprintf(" — %v", title)
	} else if codec, err := ui.m.GetProperty("current-tracks/"+kind+"/codec", mpv.FormatString); err == nil {
		desc += fmt.Sprintf(" (%v)", codec)
	}
	ui.osdMsg(label + ": " + desc)
}

func (ui *UI) osdMsg(msg string) {
	ui.command("show-text", msg, "1500")
}

func (ui *UI) toggleFullscreen() {
	if ui.fullscreen {
		ui.win.SetMonitor(nil, ui.winX, ui.winY, ui.winW, ui.winH, 0)
		ui.fullscreen = false
		styleTitlebar(ui.win) // GLFW rebuilds the style mask on mode switches
		return
	}
	ui.winX, ui.winY = ui.win.GetPos()
	ui.winW, ui.winH = ui.win.GetSize()
	mon := glfw.GetPrimaryMonitor()
	mode := mon.GetVideoMode()
	ui.win.SetMonitor(mon, 0, 0, mode.Width, mode.Height, mode.RefreshRate)
	ui.fullscreen = true
}

// render pushes the channel list as an ASS overlay via mpv's osd-overlay
// command, or removes it when hidden. While panelPos/pillY animations run,
// tick() calls this every frame with interpolated offset/alpha.
func (ui *UI) render() {
	now := time.Now()
	if !ui.visible && ui.panelPos == nil {
		ui.setOverlay("none", "")
		return
	}
	frac := 0.0 // 0 = onscreen, -1 = offscreen left
	if ui.panelPos != nil {
		frac = ui.panelPos.at(now)
	}
	off := frac * float64(panelWidth+30)
	vis := 1 + frac
	var b strings.Builder

	// Translucent panel background, drawn with ASS vector commands.
	fmt.Fprintf(&b, "{\\an7\\pos(%.1f,0)\\bord0\\shad0\\1c&H101010&%s\\p1}m 0 0 l %d 0 l %d %d l 0 %d{\\p0}\n",
		off, alphaTag(0x30, vis), panelWidth, panelWidth, osdResY, osdResY)

	// Header: search filter and channel count. Monochrome; red is reserved
	// for errors only.
	header := fmt.Sprintf("%d channels — type to search", len(ui.channels))
	headerColor := "&HFFFFFF&"
	if len(ui.filter) > 0 {
		header = fmt.Sprintf("Search: %s_  (%d/%d)", escapeASS(string(ui.filter)), len(ui.view), len(ui.channels))
	}
	if ui.loadErr != nil {
		header = "Channel list unavailable — check URL / network"
		headerColor = errorColor
	}
	line := 1
	fmt.Fprintf(&b, "{\\an7\\pos(%.1f,%d)\\bord0\\shad0\\fs22\\b1\\1c%s%s}%s\n", 20+off, lineY(line), headerColor, alphaTag(0, vis), header)
	line++
	if ui.status != "" {
		statusColor, statusBase := "&HFFFFFF&", 0x60 // secondary label
		if ui.statusErr {
			statusColor, statusBase = errorColor, 0
		}
		fmt.Fprintf(&b, "{\\an7\\pos(%.1f,%d)\\bord0\\shad0\\fs20\\1c%s%s}%s\n", 20+off, lineY(line), statusColor, alphaTag(statusBase, vis), escapeASS(truncate(ui.status, 52)))
		line++
	}
	line++ // spacer

	// Selection pill: glides between rows while pillY animates.
	if ui.sel < len(ui.view) && ui.view[ui.sel].header == "" {
		pillTop := ui.pillTargetY()
		if ui.pillY != nil {
			pillTop = ui.pillY.at(now)
		}
		fmt.Fprintf(&b, "{\\an7\\pos(%.1f,0)\\bord0\\shad0\\1c&HFFFFFF&%s\\p1}m 10 %.1f l %d %.1f l %d %.1f l 10 %.1f{\\p0}\n",
			off, alphaTag(0xD8, vis), pillTop-3, panelWidth-10, pillTop-3, panelWidth-10, pillTop+27, pillTop+27)
	}

	end := ui.scroll + maxRows
	if end > len(ui.view) {
		end = len(ui.view)
	}
	for row := ui.scroll; row < end; row++ {
		it := ui.view[row]
		if it.header != "" {
			fmt.Fprintf(&b, "{\\an7\\pos(%.1f,%d)\\bord0\\shad0\\fs18\\b1\\1c&H999999&%s}%s\n", 20+off, lineY(line)+4, alphaTag(0, vis), escapeASS(it.header))
			line++
			continue
		}
		name := escapeASS(ui.channels[it.chanIdx].Name)
		if it.chanIdx == ui.current {
			name += "  ●"
		}
		if row == ui.sel {
			// Selection: bold + marker (shape and weight carry the state).
			fmt.Fprintf(&b, "{\\an7\\pos(%.1f,%d)\\bord0\\shad0\\fs24\\b1\\1c&HFFFFFF&%s}▶\\h%s\n", 20+off, lineY(line), alphaTag(0, vis), name)
		} else {
			fmt.Fprintf(&b, "{\\an7\\pos(%.1f,%d)\\bord0\\shad0\\fs24\\1c&HFFFFFF&%s}\\h\\h%s\n", 20+off, lineY(line), alphaTag(0, vis), name)
		}
		line++
	}

	fmt.Fprintf(&b, "{\\an7\\pos(%.1f,%d)\\bord0\\shad0\\fs16\\1c&HAAAAAA&%s}↑↓ select   ⏎ play   Tab hide   f fullscreen   q quit\n", 20+off, osdResY-28, alphaTag(0, vis))

	ui.setOverlay("ass-events", b.String())
}

// truncate limits s to n runes so long status lines stay inside the panel.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func lineY(line int) int {
	return 8 + line*28
}

func (ui *UI) setOverlay(format, data string) {
	ui.setOverlayID(listOverlayID, format, data)
}

func (ui *UI) setOverlayID(id int64, format, data string) {
	_, err := ui.m.CommandNode(map[string]any{
		"name":   "osd-overlay",
		"id":     id,
		"format": format,
		"data":   data,
		"res_x":  int64(osdResX),
		"res_y":  int64(osdResY),
	})
	if err != nil {
		log.Printf("osd-overlay: %v", err)
	}
}

// escapeASS strips characters that would be interpreted as ASS override
// tags or line breaks from untrusted channel names.
func escapeASS(s string) string {
	r := strings.NewReplacer("{", "(", "}", ")", "\\", "/", "\n", " ")
	return r.Replace(s)
}
