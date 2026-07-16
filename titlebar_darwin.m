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
