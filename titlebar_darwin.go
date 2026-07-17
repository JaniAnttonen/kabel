package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdbool.h>
#include <stdlib.h>
void kabelStyleTitlebar(void *win);
void kabelInfoBarText(void *win, const char *line1, const char *line2);
void kabelInfoBarShow(void *win, bool show);
void kabelSetTitle(void *win, const char *text);
void kabelApplyLuma(void *win, double topLuma, double botLuma);
*/
import "C"

import (
	"unsafe"

	"github.com/go-gl/glfw/v3.3/glfw"
)

// styleTitlebar applies the native overlay-titlebar treatment: content flush
// under a Liquid Glass titlebar that fades out while the window is inactive.
// Must run on the main thread (the GLFW event loop thread).
func styleTitlebar(win *glfw.Window) {
	C.kabelStyleTitlebar(win.GetCocoaWindow())
}

// infoBarText sets the two lines of the bottom EPG bar (main thread only).
func infoBarText(win *glfw.Window, line1, line2 string) {
	c1, c2 := C.CString(line1), C.CString(line2)
	defer C.free(unsafe.Pointer(c1))
	defer C.free(unsafe.Pointer(c2))
	C.kabelInfoBarText(win.GetCocoaWindow(), c1, c2)
}

// infoBarShow fades the bottom EPG bar in or out (main thread only).
func infoBarShow(win *glfw.Window, show bool) {
	C.kabelInfoBarShow(win.GetCocoaWindow(), C.bool(show))
}

// setWindowTitle sets the adaptive titlebar label (main thread only).
func setWindowTitle(win *glfw.Window, text string) {
	c := C.CString(text)
	defer C.free(unsafe.Pointer(c))
	C.kabelSetTitle(win.GetCocoaWindow(), c)
}

// applyLuma updates backdrop luminance for the titlebar (top) and info bar
// (bottom); pass a negative value to leave one unchanged (main thread only).
func applyLuma(win *glfw.Window, top, bottom float64) {
	C.kabelApplyLuma(win.GetCocoaWindow(), C.double(top), C.double(bottom))
}
