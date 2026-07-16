package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/gen2brain/go-mpv"
	"github.com/go-gl/glfw/v3.3/glfw"
)

// ASS coordinate space for the OSD overlay; mpv scales it to the window.
const (
	osdResX    = 1280
	osdResY    = 720
	panelWidth = 500
	maxRows    = 22
)

type UI struct {
	m   *mpv.Mpv
	win *glfw.Window

	channels []Channel
	loadErr  error

	filter   []rune
	filtered []int // indexes into channels matching filter
	sel      int   // selection within filtered
	scroll   int   // first visible row within filtered

	visible bool
	current int // index into channels currently playing, -1 = none
	status  string

	fullscreen             bool
	winX, winY, winW, winH int
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
	if idx < 0 || idx >= len(ui.channels) {
		return
	}
	c := ui.channels[idx]
	if err := ui.m.Command([]string{"loadfile", c.URL}); err != nil {
		ui.status = "Failed to start: " + c.Name
		log.Printf("loadfile %s: %v", c.URL, err)
		ui.render()
		return
	}
	ui.current = idx
	ui.status = "Tuning " + c.Name + "…"
	ui.win.SetTitle(c.Name + " — Fritz!Box TV")
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
		ui.render()
	}
}

func (ui *UI) playbackEnded() {
	// mpv went idle: either the idle source was replaced (fine) or a stream
	// died. If a channel was playing, surface that and bring the list back.
	if ui.current >= 0 {
		idleActive, err := ui.m.GetProperty("idle-active", mpv.FormatFlag)
		if err == nil && idleActive == false {
			return // just a track switch, not idle
		}
		ui.status = "Playback ended: " + ui.channels[ui.current].Name
		ui.current = -1
		ui.win.SetTitle("Fritz!Box TV")
		if err := ui.m.Command([]string{"loadfile", idleSource}); err != nil {
			log.Printf("idle source: %v", err)
		}
		ui.showList()
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
		ui.step(1)
	case glfw.KeyDown:
		ui.step(-1)
	case glfw.KeyLeft:
		ui.command("add", "volume", "-5")
		ui.osdMsg("Volume ${volume}%")
	case glfw.KeyRight:
		ui.command("add", "volume", "5")
		ui.osdMsg("Volume ${volume}%")
	case glfw.KeyM:
		ui.command("cycle", "mute")
		ui.osdMsg("Mute: ${mute}")
	case glfw.KeySpace:
		ui.command("cycle", "pause")
	case glfw.KeyF:
		ui.toggleFullscreen()
	case glfw.KeyQ:
		ui.win.SetShouldClose(true)
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

	// Header: search filter and channel count.
	header := fmt.Sprintf("%d channels — type to search", len(ui.channels))
	if len(ui.filter) > 0 {
		header = fmt.Sprintf("Search: %s_  (%d/%d)", escapeASS(string(ui.filter)), len(ui.filtered), len(ui.channels))
	}
	if ui.loadErr != nil {
		header = "Channel list unavailable — check Fritz!Box"
	}
	line := 1
	fmt.Fprintf(&b, "{\\an7\\pos(20,%d)\\bord0\\shad0\\fs22\\b1\\1c&H00D7FF&}%s\n", lineY(line), header)
	line++
	if ui.status != "" {
		fmt.Fprintf(&b, "{\\an7\\pos(20,%d)\\bord0\\shad0\\fs20\\1c&H8080FF&}%s\n", lineY(line), escapeASS(ui.status))
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
		color, marker := "&HFFFFFF&", "\\h\\h"
		if row == ui.sel {
			color, marker = "&H00D7FF&", "▶\\h"
		}
		if idx == ui.current {
			name += "  ●"
		}
		fmt.Fprintf(&b, "{\\an7\\pos(20,%d)\\bord0\\shad0\\fs24\\1c%s}%s%s\n", lineY(line), color, marker, name)
		line++
	}

	fmt.Fprintf(&b, "{\\an7\\pos(20,%d)\\bord0\\shad0\\fs16\\1c&HAAAAAA&}↑↓ select   ⏎ play   Tab hide   f fullscreen   q quit\n", osdResY-28)

	ui.setOverlay("ass-events", b.String())
}

func lineY(line int) int {
	return 8 + line*28
}

func (ui *UI) setOverlay(format, data string) {
	_, err := ui.m.CommandNode(map[string]any{
		"name":   "osd-overlay",
		"id":     int64(1),
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
