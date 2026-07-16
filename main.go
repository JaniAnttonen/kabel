package main

import (
	"flag"
	"log"
	"os"
	"runtime"
	"time"
	"unsafe"

	"github.com/gen2brain/go-mpv"
	"github.com/go-gl/glfw/v3.3/glfw"
)

// Default channel list: iptv-org's community catalog of publicly available
// streams. Point -url / KABEL_M3U at the Fritz!Box for live DVB-C TV,
// e.g. http://192.168.178.1/dvb/m3u/tv.m3u
const defaultM3UURL = "https://iptv-org.github.io/iptv/index.m3u"

// idleSource is a synthetic black video played while no channel is selected,
// so the VO is configured and the OSD channel list has something to render on.
const idleSource = "av://lavfi:color=c=black:s=1280x720"

func init() {
	// GLFW (Cocoa) event handling must stay on the main OS thread.
	runtime.LockOSThread()
}

func main() {
	log.SetFlags(log.Ltime)
	urlFlag := flag.String("url", envOr("KABEL_M3U", defaultM3UURL), "URL of the m3u channel list")
	autoplay := flag.Bool("autoplay", false, "start playing the first channel immediately")
	flag.Parse()

	channels, loadErr := loadChannels(*urlFlag)
	if loadErr != nil {
		log.Printf("channel list: %v", loadErr)
	} else {
		log.Printf("loaded %d channels from %s", len(channels), *urlFlag)
	}

	if err := glfw.Init(); err != nil {
		log.Fatalf("glfw init: %v", err)
	}
	log.Printf("glfw initialized")
	defer glfw.Terminate()

	glfw.WindowHint(glfw.CocoaRetinaFramebuffer, glfw.True)
	// mpv's GL renderer needs >= 3.0 for hwdec (VideoToolbox) interop;
	// macOS only offers 3.2+ as core profile.
	glfw.WindowHint(glfw.ContextVersionMajor, 3)
	glfw.WindowHint(glfw.ContextVersionMinor, 2)
	glfw.WindowHint(glfw.OpenGLProfile, glfw.OpenGLCoreProfile)
	glfw.WindowHint(glfw.OpenGLForwardCompatible, glfw.True)
	win, err := glfw.CreateWindow(1280, 720, "kabel", nil, nil)
	if err != nil {
		log.Fatalf("create window: %v", err)
	}
	win.MakeContextCurrent()
	glfw.SwapInterval(1)
	styleTitlebar(win)

	m := mpv.New()
	opts := map[string]string{
		// Force rendering through our render context; without this, macOS
		// mpv builds default to cocoa-cb (own window) which deadlocks
		// against GLFW's event loop.
		"vo":                     "libmpv",
		"idle":                   "yes",
		"keep-open":              "no",
		"input-default-bindings": "no",
		"osc":                    "no",
		"audio-display":          "no",
		// copy-back hwdec: hardware decode with the plain GL upload path,
		// so screenshots and filters keep working.
		"hwdec":                  "auto-copy-safe",
		"screenshot-sw":          "yes", // window screenshots despite core-profile GL
		"network-timeout":        "10",
		"cache":                  "yes",
		"demuxer-readahead-secs": "2",
		"volume":                 "100",
		"volume-max":             "100",
	}
	if logFile := os.Getenv("KABEL_MPV_LOG"); logFile != "" {
		opts["log-file"] = logFile
	}
	for k, v := range opts {
		if err := m.SetOptionString(k, v); err != nil {
			log.Printf("mpv option %s=%s: %v", k, v, err)
		}
	}
	if err := m.Initialize(); err != nil {
		log.Fatalf("mpv initialize: %v", err)
	}
	log.Printf("mpv initialized")
	defer m.TerminateDestroy()

	// Surface mpv warnings/errors on stderr and keep the last error for the
	// UI, so stream failures are diagnosable without a verbose log file.
	if err := m.RequestLogMessages("warn"); err != nil {
		log.Printf("mpv log messages: %v", err)
	}

	rc, err := m.NewRenderContextGL(func(name string) unsafe.Pointer {
		return glfw.GetProcAddress(name)
	})
	if err != nil {
		log.Fatalf("mpv render context: %v", err)
	}
	defer rc.Free()
	log.Printf("render context created")

	// Wake the GLFW event loop whenever mpv has a new frame; PostEmptyEvent
	// is one of the few GLFW calls that is safe from any thread.
	rc.SetUpdateCallback(glfw.PostEmptyEvent)

	ui := newUI(m, win, channels, loadErr)

	needsRender := true
	win.SetFramebufferSizeCallback(func(_ *glfw.Window, _, _ int) {
		needsRender = true
	})
	win.SetKeyCallback(func(_ *glfw.Window, key glfw.Key, _ int, action glfw.Action, mods glfw.ModifierKey) {
		if action == glfw.Press || action == glfw.Repeat {
			ui.handleKey(key, mods)
		}
	})
	win.SetCharCallback(func(_ *glfw.Window, r rune) {
		ui.handleChar(r)
	})
	win.SetScrollCallback(func(_ *glfw.Window, _, yoff float64) {
		ui.handleScroll(yoff)
	})

	if err := m.Command([]string{"loadfile", idleSource}); err != nil {
		log.Printf("idle source: %v", err)
	}
	ui.showList()
	if *autoplay && len(channels) > 0 {
		ui.play(0)
	}

	// Keep sources fresh across network changes; Fritz!Box discovery only
	// applies alongside the default public list (an explicit -url wins).
	discover := *urlFlag == defaultM3UURL
	updates := watchSources(*urlFlag, loadErr == nil, discover, glfw.PostEmptyEvent)

	// Debug hook: KABEL_DEBUG_SHOT=/path.png captures window+OSD after 4s.
	shotPath := os.Getenv("KABEL_DEBUG_SHOT")
	shotAt := time.Now().Add(4 * time.Second)
	animating := false

	for !win.ShouldClose() {
		if shotPath != "" && time.Now().After(shotAt) {
			// window mode includes the OSD but can fail with direct hwdec
			// surfaces; fall back to the bare decoded frame.
			if err := m.Command([]string{"screenshot-to-file", shotPath, "window"}); err != nil {
				log.Printf("debug screenshot (window): %v", err)
				if err := m.Command([]string{"screenshot-to-file", shotPath, "video"}); err != nil {
					log.Printf("debug screenshot (video): %v", err)
				}
			}
			shotPath = ""
		}
		// Tight timeout while UI animations run, relaxed when idle.
		timeout := 0.1
		if animating {
			timeout = 1.0 / 120
		}
		glfw.WaitEventsTimeout(timeout)
		animating = ui.tick()

		select {
		case u := <-updates:
			if u.public != nil {
				ui.setPublic(u.public)
			}
			if u.localSet {
				ui.setLocal(u.local)
			}
		default:
		}

		for {
			ev := m.WaitEvent(0)
			if ev == nil || ev.EventID == mpv.EventNone {
				break
			}
			switch ev.EventID {
			case mpv.EventShutdown:
				win.SetShouldClose(true)
			case mpv.EventLogMsg:
				lm := ev.LogMessage()
				log.Printf("mpv %s [%s] %s", lm.Prefix, lm.Level, lm.Text)
				ui.noteMpvLog(lm)
			case mpv.EventEnd:
				ui.playbackEnded(ev.EndFile())
			case mpv.EventFileLoaded:
				if path, err := m.GetProperty("path", mpv.FormatString); err == nil {
					log.Printf("mpv: loaded %v", path)
				}
				ui.playbackStarted()
			case mpv.EventVideoReconfig:
				log.Printf("mpv: video configured")
			}
		}

		if rc.Update()&mpv.RenderUpdateFrame != 0 || needsRender {
			needsRender = false
			w, h := win.GetFramebufferSize()
			if err := rc.RenderGL(0, w, h, true); err != nil {
				log.Printf("render: %v", err)
			}
			win.SwapBuffers()
			rc.ReportSwap()
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
