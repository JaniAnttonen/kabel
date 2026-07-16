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

	channels []Channel
	loadErr  error

	filter   []rune
	filtered []int // indexes into channels matching filter
	sel      int   // selection within filtered
	scroll   int   // first visible row within filtered

	visible   bool
	current   int // index into channels currently playing, -1 = none
	status    string
	statusErr bool // status describes a failure (rendered in red)

	rtspTCP    bool   // use RTSP-over-TCP (set after a failed UDP attempt)
	retried    bool   // current play attempt is already the TCP retry
	lastMpvErr string // most recent error-level mpv log line

	fullscreen             bool
	winX, winY, winW, winH int

	scrollAccum float64     // fractional scroll-wheel ticks not yet applied
	volTimer    *time.Timer // hides the volume indicator overlay
}

func newUI(m *mpv.Mpv, win *glfw.Window, channels []Channel, loadErr error) *UI {
	ui := &UI{m: m, win: win, channels: channels, loadErr: loadErr, current: -1}
	ui.applyFilter()
	return ui
}

// setChannels swaps in a freshly fetched channel list (network watcher).
// The currently playing channel keeps playing; its index is re-resolved by
// URL so the ● marker stays correct.
func (ui *UI) setChannels(channels []Channel) {
	var currentURL string
	if ui.current >= 0 && ui.current < len(ui.channels) {
		currentURL = ui.channels[ui.current].URL
	}
	ui.channels = channels
	ui.loadErr = nil
	ui.current = -1
	for i, c := range channels {
		if currentURL != "" && c.URL == currentURL {
			ui.current = i
			break
		}
	}
	ui.applyFilter()
	log.Printf("channel list updated: %d channels", len(channels))
	if ui.visible {
		ui.render()
	} else {
		ui.osdMsg(fmt.Sprintf("Channel list updated (%d channels)", len(channels)))
	}
}

func (ui *UI) showList() {
	ui.visible = true
	ui.render()
}

func (ui *UI) hideList() {
	ui.visible = false
	ui.render()
}

func (ui *UI) applyFilter() {
	needle := strings.ToLower(string(ui.filter))
	ui.filtered = ui.filtered[:0]
	for i, c := range ui.channels {
		if needle == "" || strings.Contains(strings.ToLower(c.Name), needle) {
			ui.filtered = append(ui.filtered, i)
		}
	}
	if ui.sel >= len(ui.filtered) {
		ui.sel = len(ui.filtered) - 1
	}
	if ui.sel < 0 {
		ui.sel = 0
	}
	ui.clampScroll()
}

func (ui *UI) clampScroll() {
	if ui.sel < ui.scroll {
		ui.scroll = ui.sel
	}
	if ui.sel >= ui.scroll+maxRows {
		ui.scroll = ui.sel - maxRows + 1
	}
	if ui.scroll < 0 {
		ui.scroll = 0
	}
}

func (ui *UI) moveSel(delta int) {
	if len(ui.filtered) == 0 {
		return
	}
	ui.sel += delta
	if ui.sel < 0 {
		ui.sel = 0
	}
	if ui.sel >= len(ui.filtered) {
		ui.sel = len(ui.filtered) - 1
	}
	ui.clampScroll()
	ui.render()
}

func (ui *UI) play(idx int) {
	ui.playAttempt(idx, false)
}

func (ui *UI) playAttempt(idx int, isRetry bool) {
	if idx < 0 || idx >= len(ui.channels) {
		return
	}
	c := ui.channels[idx]
	if strings.HasPrefix(c.URL, "rtsp") {
		transport := "udp"
		if ui.rtspTCP {
			transport = "tcp"
		}
		if err := ui.m.SetOptionString("rtsp-transport", transport); err != nil {
			log.Printf("rtsp-transport=%s: %v", transport, err)
		}
	}
	ui.lastMpvErr = ""
	ui.retried = isRetry
	if err := ui.m.Command([]string{"loadfile", c.URL}); err != nil {
		ui.status = "Failed to start: " + c.Name
		ui.statusErr = true
		log.Printf("loadfile %s: %v", c.URL, err)
		ui.render()
		return
	}
	ui.current = idx
	ui.statusErr = false
	if isRetry {
		ui.status = "Retrying " + c.Name + " via TCP…"
	} else {
		ui.status = "Tuning " + c.Name + "…"
	}
	ui.win.SetTitle(c.Name + " — kabel")
	ui.hideList()
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

	// Fritz!Box streams are RTSP with RTP over UDP by default; if UDP data
	// never arrives (firewall, packet loss) or the tuner was momentarily
	// busy, one retry over TCP usually rescues it.
	if !ui.retried && strings.HasPrefix(c.URL, "rtsp") {
		ui.rtspTCP = true
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
			if len(ui.filtered) > 0 {
				ui.play(ui.filtered[ui.sel])
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

// showVolume draws a circular volume indicator (progress ring with the
// percentage centered) in the lower-right corner and fades it after 1.5s.
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
	ui.setOverlayID(volumeOverlayID, "ass-events", volumeASS(v, muted))
	if ui.volTimer != nil {
		ui.volTimer.Stop()
	}
	// The callback runs off the main thread but only issues mpv commands,
	// which are thread-safe; it must not touch other UI state.
	ui.volTimer = time.AfterFunc(1500*time.Millisecond, func() {
		ui.setOverlayID(volumeOverlayID, "none", "")
	})
}

// volumeASS renders the indicator: backdrop disc, faint full ring, progress
// arc from 12 o'clock, and the percentage (or mute mark) in the middle.
func volumeASS(v float64, muted bool) string {
	const cx, cy = float64(osdResX - 100), float64(osdResY - 100)
	const rOut, rIn = 46.0, 37.0
	var b strings.Builder
	fmt.Fprintf(&b, "{\\an7\\pos(0,0)\\bord0\\shad0\\1c&H101010&\\1a&H38&\\p1}%s{\\p0}\n",
		circlePath(cx, cy, 58))
	fmt.Fprintf(&b, "{\\an7\\pos(0,0)\\bord0\\shad0\\1c&H666666&\\1a&H90&\\p1}%s{\\p0}\n",
		ringPath(cx, cy, rOut, rIn, 0, 360))
	color := "&HFFFFFF&"
	label := fmt.Sprintf("%.0f%%", v)
	if muted {
		color = "&H888888&"
		label = "✕"
	}
	if v > 0 {
		fmt.Fprintf(&b, "{\\an7\\pos(0,0)\\bord0\\shad0\\1c%s\\p1}%s{\\p0}\n",
			color, ringPath(cx, cy, rOut, rIn, -90, -90+3.6*v))
	}
	fmt.Fprintf(&b, "{\\an5\\pos(%.0f,%.0f)\\bord0\\shad0\\fs24\\b1\\1c&HFFFFFF&}%s\n",
		cx, cy, label)
	return b.String()
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
// command, or removes it when hidden.
func (ui *UI) render() {
	if !ui.visible {
		ui.setOverlay("none", "")
		return
	}
	var b strings.Builder

	// Translucent panel background, drawn with ASS vector commands.
	fmt.Fprintf(&b, "{\\an7\\pos(0,0)\\bord0\\shad0\\1c&H101010&\\1a&H30&\\p1}m 0 0 l %d 0 l %d %d l 0 %d{\\p0}\n",
		panelWidth, panelWidth, osdResY, osdResY)

	// Header: search filter and channel count. Monochrome; red is reserved
	// for errors only.
	header := fmt.Sprintf("%d channels — type to search", len(ui.channels))
	headerColor := "&HFFFFFF&"
	if len(ui.filter) > 0 {
		header = fmt.Sprintf("Search: %s_  (%d/%d)", escapeASS(string(ui.filter)), len(ui.filtered), len(ui.channels))
	}
	if ui.loadErr != nil {
		header = "Channel list unavailable — check URL / network"
		headerColor = errorColor
	}
	line := 1
	fmt.Fprintf(&b, "{\\an7\\pos(20,%d)\\bord0\\shad0\\fs22\\b1\\1c%s}%s\n", lineY(line), headerColor, header)
	line++
	if ui.status != "" {
		statusColor, statusAlpha := "&HFFFFFF&", "\\1a&H60&" // secondary label
		if ui.statusErr {
			statusColor, statusAlpha = errorColor, ""
		}
		fmt.Fprintf(&b, "{\\an7\\pos(20,%d)\\bord0\\shad0\\fs20\\1c%s%s}%s\n", lineY(line), statusColor, statusAlpha, escapeASS(truncate(ui.status, 52)))
		line++
	}
	line++ // spacer

	end := ui.scroll + maxRows
	if end > len(ui.filtered) {
		end = len(ui.filtered)
	}
	for row := ui.scroll; row < end; row++ {
		idx := ui.filtered[row]
		name := escapeASS(ui.channels[idx].Name)
		if idx == ui.current {
			name += "  ●"
		}
		if row == ui.sel {
			// Selection: translucent pill + bold + marker (shape and weight
			// carry the state, not color).
			y := lineY(line)
			fmt.Fprintf(&b, "{\\an7\\pos(0,0)\\bord0\\shad0\\1c&HFFFFFF&\\1a&HD8&\\p1}m 10 %d l %d %d l %d %d l 10 %d{\\p0}\n",
				y-3, panelWidth-10, y-3, panelWidth-10, y+27, y+27)
			fmt.Fprintf(&b, "{\\an7\\pos(20,%d)\\bord0\\shad0\\fs24\\b1\\1c&HFFFFFF&}▶\\h%s\n", y, name)
		} else {
			fmt.Fprintf(&b, "{\\an7\\pos(20,%d)\\bord0\\shad0\\fs24\\1c&HFFFFFF&}\\h\\h%s\n", lineY(line), name)
		}
		line++
	}

	fmt.Fprintf(&b, "{\\an7\\pos(20,%d)\\bord0\\shad0\\fs16\\1c&HAAAAAA&}↑↓ select   ⏎ play   Tab hide   f fullscreen   q quit\n", osdResY-28)

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
