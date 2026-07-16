#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

static char kObserversKey;

static NSView *titlebarView(NSWindow *w) {
    return [[w standardWindowButton:NSWindowCloseButton] superview];
}

static void setTitlebarShown(NSWindow *w, BOOL shown) {
    NSView *tb = titlebarView(w);
    if (tb == nil) {
        return;
    }
    [NSAnimationContext runAnimationGroup:^(NSAnimationContext *ctx) {
        ctx.duration = 0.20;
        tb.animator.alphaValue = shown ? 1.0 : 0.0;
    }];
}

// kabelStyleTitlebar makes the titlebar an overlay on the content (video
// extends beneath it), backs it with the system's glass material, and hides
// it whenever the window is not key. Idempotent; call again after leaving
// fullscreen since GLFW rebuilds the style mask.
void kabelStyleTitlebar(void *win) {
    NSWindow *w = (__bridge NSWindow *)win;
    w.styleMask |= NSWindowStyleMaskFullSizeContentView;
    w.titlebarAppearsTransparent = YES;

    NSView *tb = titlebarView(w);
    if (tb != nil) {
        BOOL hasGlass = NO;
        for (NSView *sub in tb.subviews) {
            if ([sub.identifier isEqualToString:@"kabel-glass"]) {
                hasGlass = YES;
                break;
            }
        }
        if (!hasGlass) {
            NSView *glass = nil;
            Class glassCls = NSClassFromString(@"NSGlassEffectView");
            if (glassCls != nil) { // macOS 26+ Liquid Glass
                glass = [[glassCls alloc] initWithFrame:tb.bounds];
            } else {
                NSVisualEffectView *ev = [[NSVisualEffectView alloc] initWithFrame:tb.bounds];
                ev.material = NSVisualEffectMaterialTitlebar;
                ev.blendingMode = NSVisualEffectBlendingModeWithinWindow;
                ev.state = NSVisualEffectStateFollowsWindowActiveState;
                glass = ev;
            }
            glass.identifier = @"kabel-glass";
            glass.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
            [tb addSubview:glass positioned:NSWindowBelow relativeTo:nil];
        }
        tb.alphaValue = w.keyWindow ? 1.0 : 0.0;
    }

    if (objc_getAssociatedObject(w, &kObserversKey) == nil) {
        objc_setAssociatedObject(w, &kObserversKey, @YES, OBJC_ASSOCIATION_RETAIN);
        NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
        [nc addObserverForName:NSWindowDidBecomeKeyNotification
                        object:w
                         queue:[NSOperationQueue mainQueue]
                    usingBlock:^(NSNotification *n) { setTitlebarShown(w, YES); }];
        [nc addObserverForName:NSWindowDidResignKeyNotification
                        object:w
                         queue:[NSOperationQueue mainQueue]
                    usingBlock:^(NSNotification *n) { setTitlebarShown(w, NO); }];
    }
}
