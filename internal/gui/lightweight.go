//go:build !nogui

package gui

import (
	"cmp"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/egoist/mygo"

	"github.com/yetone/magpie/internal/omarchy"
	"github.com/yetone/magpie/internal/settings"
)

// The windows, h.main and h.panel, are nil while lightweight mode has let
// them go (lightweight_rule.go). Both are read and set on the main thread
// only (mygo.RunOnMain, which runs at once when already on it), so a window
// is never used after it has gone. h.winMu is held, never on the main
// thread, around what has to reach a window from elsewhere and wait for the
// main thread itself (TintPanel's dispatch_sync), and around letting a
// window go.

// makeMain makes the main window, hidden, on url, with its hooks.
func (h *host) makeMain(url string) *mygo.Window {
	z := h.zoom()
	// the window opens at the size it was last given, no smaller than its
	// page's least at the text size (window_rule.go); maximised again, if it
	// was: on Linux as it is made, elsewhere once it is first shown
	// (placeMain)
	minW, minH := windowMin(z, 0, 0)
	width, height := openSize(settings.Load().Window, minW, minH)
	theme := cmp.Or(os.Getenv("MAGPIE_THEME"), settings.Load().Theme)
	w := mygo.NewWindow(mygo.WindowOptions{
		Title:     "magpie",
		URL:       url,
		Width:     width,
		Height:    height,
		MinWidth:  minW,
		MinHeight: minH,
		Hidden:    true,
		Maximized: runtime.GOOS == "linux" && settings.Load().WindowMaximised,
		// no title bar drawn on the Mac, the traffic lights over the page's
		// header; the header marks what drags
		TitleBarStyle: mygo.TitleBarHiddenInset,
		// Windows' title bar in the page's colour from the first frame; the
		// page keeps it so as its theme changes (TintTitleBar)
		BackgroundColor: windowChrome(theme),
		Page:            mygo.PageOptions{ZoomFactor: z},
	})
	tintNew(w, theme)
	// A resize is kept once it settles (settle): the size, or that the
	// window is maximised. Nothing is kept of a window not placed and shown
	// yet (placeMain: a text size can resize it, unmaximised as yet), or
	// one full screen or minimised, which keeps what was kept before it went
	// so; nor, on Linux, of one hidden by then, since X11 drops a hidden
	// window's maximised state.
	var resized *time.Timer
	w.OnResize(func() {
		if resized != nil {
			resized.Stop()
		}
		resized = time.AfterFunc(500*time.Millisecond, func() {
			mygo.RunOnMain(func() {
				if h.placed.Load() != w || w.IsDestroyed() || w.IsFullScreen() || w.IsMinimized() || runtime.GOOS == "linux" && !w.IsVisible() {
					return
				}
				wd, ht := w.Size()
				if wd == 0 {
					return
				}
				sw, sh := fitScreen(w)
				if s := settings.Load(); settle(&s, wd, ht, sw, sh, w.IsMaximized()) {
					settings.Save(s)
				}
			})
		})
	})
	// Closing the window keeps the tray alive; quitting is a menu action.
	w.OnClose(func(e *mygo.CloseEvent) {
		if h.quitting.Load() {
			return
		}
		e.PreventDefault()
		if closeStep(runtime.GOOS, w.IsFullScreen()) == closeLeaveFullscreen {
			go h.leaveFullscreenThenHide()
			return
		}
		go h.hideMain()
	})
	// Out of full screen, magpie leaves the Dock again if it was there only
	// for that (dockOnFullscreen).
	w.OnLeaveFullScreen(func() {
		go func() {
			if h.closing.Swap(false) {
				h.hideMain()
				return
			}
			h.dock(settings.Load(), true)
		}()
	})
	plainTitlebar(w) // Linux: the page's header is the title bar
	return w
}

// makePanel makes the tray panel, at the height it last had: a flyout by
// the tray icon, which places it and closes it as a menu closes. Linux's
// tray tells nothing of where it is, and Omarchy's window managers place
// the panel by its title (omarchy.PlacePanel, omarchy.NiriPanel): there it
// is a window of its own, frameless, over the others.
func (h *host) makePanel() *mygo.Window {
	z := h.zoom()
	url := "/?mode=panel" + h.query
	width, height := h.panelW(), cmp.Or(h.panelHeight, zoomed(panelStart, z))
	page := mygo.PageOptions{ZoomFactor: z}
	var w *mygo.Window
	if runtime.GOOS == "linux" || h.tray == nil {
		w = mygo.NewWindow(mygo.WindowOptions{
			Title:         panelTitle,
			URL:           url,
			Width:         width,
			Height:        height,
			Hidden:        true,
			Frameless:     true,
			AlwaysOnTop:   true,
			DisableResize: true,
			SkipTaskbar:   true,
			Transparent:   true,
			Page:          page,
		})
		// Hyprland gives it no focus to lose (see omarchy.ReportClicks)
		if !omarchy.Hyprland() {
			w.OnBlur(func() { w.Hide() })
		}
	} else {
		w = mygo.NewFlyout(mygo.FlyoutOptions{
			Tray:      h.tray,
			Placement: mygo.PlacementBottom,
			Gap:       6,
			Width:     width,
			Height:    height,
			Focusable: true,
			Hidden:    true,
			Shadow:    true,
			URL:       url,
			Page:      page,
		})
	}
	if runtime.GOOS == "darwin" {
		w.SetVibrancy(mygo.VibrancyPopover)
	}
	// the icon stays lit while the panel is open (the Mac's)
	trayOwnClicks()
	w.OnShow(func() { trayHighlight(true) })
	w.OnHide(func() { trayHighlight(false) })
	// Closing hides the panel: a press outside the flyout, Escape, a title
	// bar's X (KWin's, #1283), Alt+F4 or the window manager's close key.
	// Closed, it was gone while the tray still opened it, and the icon did
	// nothing until magpie restarted.
	w.OnClose(func(e *mygo.CloseEvent) {
		if h.quitting.Load() {
			return
		}
		e.PreventDefault()
		w.Hide()
		if omarchy.Hyprland() {
			go omarchy.StopClicks()
		}
	})
	ownFrame(w) // KDE: no title bar of KWin's on it (#1283)
	return w
}

// lighten looks at the closed windows every lightEvery and lets go of one
// closed long enough while lightweight mode is on (#580).
func (h *host) lighten() {
	<-h.ready
	var ticks [2]int // the main window's, the panel's
	for range time.Tick(lightEvery) {
		on := settings.Load().Lightweight
		h.winMu.Lock()
		mygo.RunOnMain(func() {
			for i, wp := range []**mygo.Window{&h.main, &h.panel} {
				w := *wp
				if w == nil {
					ticks[i] = 0
					continue
				}
				var release bool
				if ticks[i], release = lightStep(on, h.lightShown(w), ticks[i]); !release {
					continue
				}
				ticks[i] = 0
				*wp = nil
				if i == 0 {
					h.placed.CompareAndSwap(w, nil)
				}
				log.Printf("lightweight: the %s window's webview let go", [2]string{"main", "panel"}[i])
				w.Destroy()
			}
		})
		h.winMu.Unlock()
	}
}

// panelWin is the panel, made again if lightweight mode let it go; on the
// main thread.
func (h *host) panelWin() *mygo.Window {
	if h.panel == nil {
		h.panel = h.makePanel()
	}
	return h.panel
}

// lightShown says whether lightweight mode keeps w: it is up (windowUp),
// or it is the main window leaving full screen to be closed; on the main
// thread. A window open under another app's was let go as closed, and
// Command-Tab back to magpie found it blank or gone (#1381).
func (h *host) lightShown(w *mygo.Window) bool {
	return windowUp(w) || w == h.main && h.closing.Load()
}

// mainShown says whether the main window is up (windowUp), covered or not;
// on the main thread.
func (h *host) mainShown() bool { return h.main != nil && windowUp(h.main) }

// openMain shows the main window on url ("" where it is), making it again
// if lightweight mode let it go; on the main thread.
func (h *host) openMain(url string) {
	h.closing.Store(false) // opened again while leaving full screen: it stays
	if h.panel != nil {
		h.panel.Hide()
	}
	h.dock(settings.Load(), true)
	if h.main == nil {
		h.main = h.makeMain(cmp.Or(url, "/?"+h.query))
	} else if url != "" {
		if err := h.main.Page().LoadURL(url); err != nil {
			log.Println("main window:", err)
		}
	}
	h.placeMain(h.main)
	showHere(h.main)
}

// reopenMain answers a click on magpie's Dock icon (#1252). A window that is
// open, on whichever Space, is left there, and magpie is activated as any
// app is: the Mac takes the user to the window's Space. One closed or
// minimised is shown as openMain shows it, on the Space the user is on.
func (h *host) reopenMain() {
	if h.main == nil || !windowOpen(h.main) {
		h.openMain("")
		return
	}
	h.closing.Store(false) // reopened while leaving full screen: it stays
	if h.panel != nil {
		h.panel.Hide()
	}
	activateApp()
}

// spaceSettle is how long a window just shown keeps moving to the active
// Space: the Mac moves it once the order to the front is committed, after
// Show has returned, and not at all if the flag is gone by then.
const spaceSettle = 500 * time.Millisecond

// showHere shows w and makes it key on the Space the user is on, wherever it
// was last; on the main thread. It moves only while being shown: left on,
// activating magpie in any way (the Dock, Command-Tab) would pull the open
// window off its own Space onto the user's (#1252).
func showHere(w *mygo.Window) {
	setMovesToActiveSpace(w, true)
	w.Show()
	time.AfterFunc(spaceSettle, func() {
		later(func() {
			if !w.IsDestroyed() {
				setMovesToActiveSpace(w, false)
			}
		})
	})
}

// placeMain puts the main window, made and not shown yet, as it was last
// left; on the main thread, before its first Show. On Windows, a size kept
// on a larger screen is fitted to this one's work area and centred: Windows
// doesn't, and the title bar would be out of reach. Then, if it was
// maximised, it is maximised again; on Linux it was made so (Maximized),
// since GTK can't be asked about a window not shown yet. Maximised while
// hidden, it zooms as it appears on the Mac. A Mac window larger than its
// screen is left to the Mac, which shows it fitted to the screen, and
// zoomed: zoomed here as well, it would restore to its own size, which the
// Mac fits to the screen again, so it couldn't be restored at all.
func (h *host) placeMain(w *mygo.Window) {
	if h.placed.Swap(w) == w || runtime.GOOS == "linux" {
		return
	}
	if w.IsMaximized() {
		return
	}
	wd, ht := w.Size()
	sw, sh := screenRoom(w)
	fw, fh, fitted := fitRoom(wd, ht, sw, sh)
	if fitted && runtime.GOOS == "windows" {
		w.SetSize(fw, fh)
		w.Center()
		fitted = false
	}
	if settings.Load().WindowMaximised && !fitted {
		w.Maximize()
	}
}

// fitScreen is the work area the main window is fitted to, in the units of
// its Size: Windows' (DIPs), 0s elsewhere — the Mac fits a window to its
// screen itself, and GTK's window manager does.
func fitScreen(w *mygo.Window) (int, int) {
	if runtime.GOOS != "windows" {
		return 0, 0
	}
	return screenRoom(w)
}
