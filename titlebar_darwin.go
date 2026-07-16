package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
void kabelStyleTitlebar(void *win);
*/
import "C"

import "github.com/go-gl/glfw/v3.3/glfw"

// styleTitlebar applies the native overlay-titlebar treatment: content flush
// under a Liquid Glass titlebar that fades out while the window is inactive.
// Must run on the main thread (the GLFW event loop thread).
func styleTitlebar(win *glfw.Window) {
	C.kabelStyleTitlebar(win.GetCocoaWindow())
}
