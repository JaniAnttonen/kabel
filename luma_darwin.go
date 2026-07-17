package main

/*
#cgo CFLAGS: -x objective-c -DGL_SILENCE_DEPRECATION
#cgo LDFLAGS: -framework OpenGL
#include <OpenGL/gl3.h>
#include <stdlib.h>

// kabelSampleLuma averages the perceptual luminance (0..1) of a w*h region
// at (x,y) of the current window framebuffer's back buffer. Must run on the
// GL context thread.
static double kabelSampleLuma(int x, int y, int w, int h) {
    if (w <= 0 || h <= 0) return -1;
    int n = w * h;
    unsigned char *buf = (unsigned char *)malloc((size_t)n * 4);
    if (!buf) return -1;
    glBindFramebuffer(GL_FRAMEBUFFER, 0);
    glReadBuffer(GL_BACK);
    glPixelStorei(GL_PACK_ALIGNMENT, 1);
    glReadPixels(x, y, w, h, GL_RGBA, GL_UNSIGNED_BYTE, buf);
    if (glGetError() != GL_NO_ERROR) { free(buf); return -1; }
    double sum = 0;
    for (int i = 0; i < n; i++) {
        sum += 0.299 * buf[i*4] + 0.587 * buf[i*4+1] + 0.114 * buf[i*4+2];
    }
    free(buf);
    return sum / n / 255.0;
}
*/
import "C"

// sampleLuma returns the average luminance (0..1) of a framebuffer strip, or
// -1 if the read failed. Coordinates are in framebuffer pixels, origin
// bottom-left.
func sampleLuma(x, y, w, h int) float64 {
	return float64(C.kabelSampleLuma(C.int(x), C.int(y), C.int(w), C.int(h)))
}
