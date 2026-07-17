#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include <stdbool.h>

static char kObserversKey;
static char kInfoBarKey;
static char kInfoLine1Key;
static char kInfoLine2Key;
static char kInfoStatusKey;
static char kTitleLabelKey;
static char kLumaTopKey;
static char kLumaBotKey;

static double lumaFor(NSWindow *w, char *key, double dflt) {
    NSNumber *n = objc_getAssociatedObject(w, key);
    return n ? n.doubleValue : dflt;
}

// refreshLabel re-renders a label's text with a luminance-adaptive colour
// (light text on dark backdrops, dark on light) plus an opposite-colour
// shadow so it stays legible even at mid grey. Text is read from the
// label's current stringValue.
static void refreshLabel(NSTextField *tf, double luma, BOOL secondary) {
    if (tf == nil) {
        return;
    }
    NSString *s = tf.stringValue ?: @"";
    BOOL dark = luma < 0.55; // dark backdrop -> light text
    CGFloat fg = dark ? 1.0 : 0.0;
    CGFloat a = secondary ? 0.80 : 1.0;
    NSShadow *sh = [[NSShadow alloc] init];
    sh.shadowColor = [NSColor colorWithWhite:(dark ? 0.0 : 1.0) alpha:0.5];
    sh.shadowBlurRadius = 3.0;
    sh.shadowOffset = NSMakeSize(0, -0.5);
    NSMutableParagraphStyle *ps = [[NSMutableParagraphStyle alloc] init];
    ps.lineBreakMode = tf.lineBreakMode;
    ps.alignment = tf.alignment; // else the attributed string forces natural (left)
    NSDictionary *attrs = @{
        NSForegroundColorAttributeName : [NSColor colorWithWhite:fg alpha:a],
        NSShadowAttributeName : sh,
        NSFontAttributeName : tf.font,
        NSParagraphStyleAttributeName : ps,
    };
    tf.attributedStringValue = [[NSAttributedString alloc] initWithString:s attributes:attrs];
}

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

// passthroughClass returns a runtime subclass of base whose hitTest: always
// returns nil, so the view is purely decorative and never intercepts mouse
// events (window dragging in particular).
static Class passthroughClass(Class base) {
    NSString *name = [NSString stringWithFormat:@"KabelPassthrough_%s", class_getName(base)];
    Class cls = NSClassFromString(name);
    if (cls != nil) {
        return cls;
    }
    cls = objc_allocateClassPair(base, name.UTF8String, 0);
    IMP nilHitTest = imp_implementationWithBlock(^NSView *(id self, NSPoint p) { return nil; });
    class_addMethod(cls, @selector(hitTest:), nilHitTest, "@@:{CGPoint=dd}");
    objc_registerClassPair(cls);
    return cls;
}

// makeViewDraggable makes cls report that a mouse-down may move the window,
// added to that class only (no effect on superclasses).
static void makeViewDraggable(Class cls) {
    SEL sel = @selector(mouseDownCanMoveWindow);
    IMP yes = imp_implementationWithBlock(^BOOL(id self) { return YES; });
    Method base = class_getInstanceMethod(cls, sel);
    if (!class_addMethod(cls, sel, yes, method_getTypeEncoding(base))) {
        method_setImplementation(class_getInstanceMethod(cls, sel), yes);
    }
}

// kabelSetupInfoBar creates (or re-attaches after a style-mask rebuild) the
// bottom EPG bar: a full-width Liquid Glass strip styled like the titlebar,
// hit-test transparent, holding two truncating text lines. Hidden until
// kabelInfoBarShow.
void kabelSetupInfoBar(void *win) {
    NSWindow *w = (__bridge NSWindow *)win;
    NSView *frame = [w.contentView superview];
    if (frame == nil) {
        return;
    }
    NSView *bar = objc_getAssociatedObject(w, &kInfoBarKey);
    if (bar != nil) {
        if (bar.superview != frame) { // theme frame was rebuilt (fullscreen)
            [bar removeFromSuperview];
            [frame addSubview:bar positioned:NSWindowAbove relativeTo:w.contentView];
        }
        return;
    }
    const CGFloat H = 56;
    NSRect r = NSMakeRect(0, 0, frame.bounds.size.width, H);
    Class glassCls = NSClassFromString(@"NSGlassEffectView");
    if (glassCls != nil) {
        bar = [[passthroughClass(glassCls) alloc] initWithFrame:r];
        @try {
            [bar setValue:@0.0 forKey:@"cornerRadius"];
        } @catch (NSException *e) {
        }
    } else {
        NSVisualEffectView *ev = [[passthroughClass([NSVisualEffectView class]) alloc] initWithFrame:r];
        ev.material = NSVisualEffectMaterialTitlebar;
        ev.blendingMode = NSVisualEffectBlendingModeWithinWindow;
        ev.state = NSVisualEffectStateFollowsWindowActiveState;
        bar = ev;
    }
    bar.identifier = @"kabel-infobar";
    bar.autoresizingMask = NSViewWidthSizable | NSViewMaxYMargin;
    bar.alphaValue = 0;

    const CGFloat statusW = 390;
    const CGFloat leftW = r.size.width - 32 - statusW - 16;

    NSTextField *l1 = [NSTextField labelWithString:@""];
    l1.font = [NSFont systemFontOfSize:13 weight:NSFontWeightSemibold];
    l1.textColor = [NSColor labelColor];
    l1.frame = NSMakeRect(16, H - 26, leftW, 18);
    l1.autoresizingMask = NSViewWidthSizable;
    l1.lineBreakMode = NSLineBreakByTruncatingTail;
    [bar addSubview:l1];

    NSTextField *l2 = [NSTextField labelWithString:@""];
    l2.font = [NSFont systemFontOfSize:11];
    l2.textColor = [NSColor secondaryLabelColor];
    l2.frame = NSMakeRect(16, 8, leftW, 16);
    l2.autoresizingMask = NSViewWidthSizable;
    l2.lineBreakMode = NSLineBreakByTruncatingTail;
    [bar addSubview:l2];

    // Right-aligned status: volume, audio track, subtitle track. Anchored
    // to the right edge (flexible left margin) so it stays put on resize.
    NSTextField *st = [NSTextField labelWithString:@""];
    st.font = [NSFont systemFontOfSize:12];
    st.alignment = NSTextAlignmentRight;
    st.frame = NSMakeRect(r.size.width - 16 - statusW, (H - 16) / 2, statusW, 16);
    st.autoresizingMask = NSViewMinXMargin | NSViewMinYMargin | NSViewMaxYMargin;
    st.lineBreakMode = NSLineBreakByTruncatingTail;
    [bar addSubview:st];

    [frame addSubview:bar positioned:NSWindowAbove relativeTo:w.contentView];
    objc_setAssociatedObject(w, &kInfoBarKey, bar, OBJC_ASSOCIATION_RETAIN);
    objc_setAssociatedObject(w, &kInfoLine1Key, l1, OBJC_ASSOCIATION_RETAIN);
    objc_setAssociatedObject(w, &kInfoLine2Key, l2, OBJC_ASSOCIATION_RETAIN);
    objc_setAssociatedObject(w, &kInfoStatusKey, st, OBJC_ASSOCIATION_RETAIN);
}

void kabelInfoBarText(void *win, const char *line1, const char *line2) {
    NSWindow *w = (__bridge NSWindow *)win;
    NSTextField *l1 = objc_getAssociatedObject(w, &kInfoLine1Key);
    NSTextField *l2 = objc_getAssociatedObject(w, &kInfoLine2Key);
    double luma = lumaFor(w, &kLumaBotKey, 0.06);
    if (l1 != nil) {
        l1.stringValue = [NSString stringWithUTF8String:line1 ?: ""];
        refreshLabel(l1, luma, NO);
    }
    if (l2 != nil) {
        l2.stringValue = [NSString stringWithUTF8String:line2 ?: ""];
        refreshLabel(l2, luma, YES);
    }
}

// kabelInfoBarStatus sets the right-aligned volume/audio/subtitle line.
void kabelInfoBarStatus(void *win, const char *text) {
    NSWindow *w = (__bridge NSWindow *)win;
    NSTextField *st = objc_getAssociatedObject(w, &kInfoStatusKey);
    if (st != nil) {
        st.stringValue = [NSString stringWithUTF8String:text ?: ""];
        refreshLabel(st, lumaFor(w, &kLumaBotKey, 0.06), NO);
    }
}

// kabelSetTitle sets the custom (adaptive) titlebar title text.
void kabelSetTitle(void *win, const char *text) {
    NSWindow *w = (__bridge NSWindow *)win;
    NSTextField *t = objc_getAssociatedObject(w, &kTitleLabelKey);
    if (t != nil) {
        t.stringValue = [NSString stringWithUTF8String:text ?: ""];
        refreshLabel(t, lumaFor(w, &kLumaTopKey, 0.06), NO);
    }
}

// kabelApplyLuma updates the sampled backdrop luminance for the titlebar
// (top) and info bar (bottom) and recolours their text accordingly.
void kabelApplyLuma(void *win, double topLuma, double botLuma) {
    NSWindow *w = (__bridge NSWindow *)win;
    if (topLuma >= 0) {
        objc_setAssociatedObject(w, &kLumaTopKey, @(topLuma), OBJC_ASSOCIATION_RETAIN);
        refreshLabel(objc_getAssociatedObject(w, &kTitleLabelKey), topLuma, NO);
    }
    if (botLuma >= 0) {
        objc_setAssociatedObject(w, &kLumaBotKey, @(botLuma), OBJC_ASSOCIATION_RETAIN);
        refreshLabel(objc_getAssociatedObject(w, &kInfoLine1Key), botLuma, NO);
        refreshLabel(objc_getAssociatedObject(w, &kInfoLine2Key), botLuma, YES);
        refreshLabel(objc_getAssociatedObject(w, &kInfoStatusKey), botLuma, NO);
    }
}

void kabelInfoBarShow(void *win, bool show) {
    NSWindow *w = (__bridge NSWindow *)win;
    NSView *bar = objc_getAssociatedObject(w, &kInfoBarKey);
    if (bar == nil) {
        return;
    }
    [NSAnimationContext runAnimationGroup:^(NSAnimationContext *ctx) {
        ctx.duration = 0.25;
        bar.animator.alphaValue = show ? 1.0 : 0.0;
    }];
}

// kabelStyleTitlebar makes the titlebar an overlay on the content (video
// extends beneath it), backs it with the system's glass material, hides it
// whenever the window is not key, and makes the window draggable from
// anywhere. Idempotent; call again after leaving fullscreen since GLFW
// rebuilds the style mask.
void kabelStyleTitlebar(void *win) {
    NSWindow *w = (__bridge NSWindow *)win;
    w.styleMask |= NSWindowStyleMaskFullSizeContentView;
    w.titlebarAppearsTransparent = YES;

    // Drag-anywhere: the GLFW content view handles mouse events, so it also
    // has to opt in to background window dragging.
    w.movableByWindowBackground = YES;
    if (w.contentView != nil) {
        makeViewDraggable([w.contentView class]);
    }

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
                glass = [[passthroughClass(glassCls) alloc] initWithFrame:tb.bounds];
                @try { // square strip, no capsule rounding at the bottom
                    [glass setValue:@0.0 forKey:@"cornerRadius"];
                } @catch (NSException *e) {
                }
            } else {
                NSVisualEffectView *ev = [[passthroughClass([NSVisualEffectView class]) alloc] initWithFrame:tb.bounds];
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

        // Replace the system title (fixed colour, illegible over video) with
        // our own centred label that recolours with the backdrop.
        if (objc_getAssociatedObject(w, &kTitleLabelKey) == nil) {
            w.titleVisibility = NSWindowTitleHidden;
            // Click-through so it never blocks title-bar window dragging.
            NSTextField *t = [[passthroughClass([NSTextField class]) alloc] initWithFrame:NSZeroRect];
            t.editable = NO;
            t.bordered = NO;
            t.drawsBackground = NO;
            t.selectable = NO;
            t.font = [NSFont systemFontOfSize:13 weight:NSFontWeightSemibold];
            t.alignment = NSTextAlignmentCenter;
            t.frame = NSMakeRect(80, (tb.bounds.size.height - 18) / 2,
                                 tb.bounds.size.width - 160, 18);
            t.autoresizingMask = NSViewWidthSizable | NSViewMinYMargin | NSViewMaxYMargin;
            t.lineBreakMode = NSLineBreakByTruncatingTail;
            [tb addSubview:t];
            objc_setAssociatedObject(w, &kTitleLabelKey, t, OBJC_ASSOCIATION_RETAIN);
        }
    }

    kabelSetupInfoBar(win);

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
