//go:build !nogui

package gui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/autostart"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/omarchy"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/shortcut"
	"github.com/yetone/magpie/internal/stats"
	"github.com/yetone/magpie/internal/update"
)

//go:embed tray.png
var trayIcon []byte // black glyph, tinted by the macOS menu bar

// trayFlap is the bird beating its wing and flicking its tail, played on
// the tray icon when it is clicked (build/icon/gen.go tray-flap).
//
//go:embed trayflap/*.png
var trayFlap embed.FS

//go:embed icon.png
var appIcon []byte // coloured, for other trays

// The app's own icon: the Mac's Dock (which it replaces the bundle's .icns
// in, at up to 512pt), window icons and the about box. At 64px it was
// scaled up there and blurred.
//
//go:embed icon-1024.png
var appIconLarge []byte

// appIconFor is the icon the app hands the system: none on a Mac whose
// bundle has AppIcon.icon compiled in (Assets.car), whose Dock then draws
// it light or dark as macOS 26 has it, where a picture set at run time
// stays light, a white tile in a dark Dock (#117).
func appIconFor() []byte {
	if runtime.GOOS == "darwin" {
		if exe, err := os.Executable(); err == nil {
			if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "..", "Resources", "Assets.car")); err == nil {
				return nil
			}
		}
	}
	return appIconLarge
}

type host struct {
	panel *mygo.Window
	main  *mygo.Window
	tray  *mygo.Tray // nil while Omarchy's bar shows magpie instead (setTrayInBar)
	menu  *mygo.Menu // the tray's
	// flapping is set while the tray bird plays its flap, so a second
	// click in it doesn't start another over it
	flapping atomic.Bool

	panelHeight int          // the panel's, in points
	panelPage   int          // what its page last asked for, in CSS pixels
	textSize    atomic.Int64 // Settings' text size, in percent (settings.TextSizes)
	hyprRoom    int          // how tall it may grow under Hyprland's bar; 0 elsewhere
	clicks      sync.Once    // Hyprland's clicks heard, to close the panel on one outside it
	glides      atomic.Int64 // the newest panel glide; older ones stop
	query       string       // what the windows' URLs carry (a forced theme)

	// niri places the panel as it opens (#1670): niriWatch starts its
	// watch once
	niri      omarchy.NiriPanel
	niriWatch sync.Once

	// closing is set while a full-screen main window, closed, leaves full
	// screen; it is hidden once it has
	closing atomic.Bool
	// quitting is set as the app quits: a window closed then closes, where
	// otherwise it is only hidden
	quitting atomic.Bool

	ready     chan struct{} // closed once the main window can be shown
	readyOnce sync.Once

	// lightweight mode (#580): see lightweight.go
	winMu sync.Mutex
	gone  atomic.Pointer[mygo.Window] // the main window let go
	// the main window placed as it was last left (placeMain), and so shown
	placed atomic.Pointer[mygo.Window]
}

// onMain runs fn on the main thread, at once when already on it, and
// returns what it does.
func onMain[T any](fn func() T) (v T) {
	mygo.RunOnMain(func() { v = fn() })
	return v
}

// later runs fn on the main thread without waiting for it.
func later(fn func()) { go mygo.RunOnMain(fn) }

// whenReady runs fn once the main window can be shown safely.
func (h *host) whenReady(fn func()) {
	go func() {
		<-h.ready
		fn()
	}()
}

func (h *host) HidePanel() {
	mygo.RunOnMain(func() {
		if h.panel != nil {
			h.panel.Hide()
		}
	})
}
func (h *host) ShowMain(view string) {
	url := ""
	if view != "" {
		url = mainURL(view, h.query)
	}
	mygo.RunOnMain(func() { h.openMain(url) })
}

// MainShown says whether the window is up.
func (h *host) MainShown() bool { return onMain(h.mainShown) }

// Import opens the window on an import link, for the user to confirm.
func (h *host) Import(link string) {
	id := stash(link)
	h.whenReady(func() {
		mygo.RunOnMain(func() { h.openMain("/?view=providers&import=" + id + h.query) })
	})
}

// dock puts magpie in the Dock or takes it out as s says, with the window
// shown or not, full screen or not.
func (h *host) dock(s settings.Settings, shown bool) {
	full := onMain(func() bool { return h.main != nil && h.main.IsFullScreen() })
	setDock(inDock(s, shown, full), shown)
}

// inDock: always, never, or while the window is shown, as s says, and while
// it is full screen whatever s says (dockOnFullscreen).
func inDock(s settings.Settings, shown, fullscreen bool) bool {
	return s.Dock || s.DockWindow && shown || fullscreen
}

func (h *host) Quit()                        { mygo.App.Quit() }
func (h *host) OpenURL(url string)           { _ = mygo.Shell.OpenExternal(url) }
func (h *host) OpenFolder(path string) error { return openFolder(path) }
func (h *host) Copy(text string) bool        { mygo.Clipboard.WriteText(text); return true }
func (h *host) ChooseFolder(title string) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:            onMain(func() *mygo.Window { return h.main }),
		Title:             title,
		Directory:         true,
		CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

// panelTitle names the panel for Hyprland's rule, which places it, and
// for niri, which finds it by it (the main window is "magpie")
const panelTitle = "magpie panel"

// FitPanel grows or shrinks the panel to its content and keeps it anchored
// under the tray icon; a shown panel glides there when g says how. height
// is the page's, in its CSS pixels: at a larger text size the panel is
// that much taller (and wider), no taller than the screen has room for.
func (h *host) FitPanel(height int, g Glide) {
	mygo.RunOnMain(func() { h.fitPanel(height, g) })
}

func (h *host) fitPanel(height int, g Glide) {
	h.panelPage = height
	_, height = panelFrame(height, h.zoom(), h.panelRoom())
	if h.panelHeight == height {
		return
	}
	h.panelHeight = height
	if h.panel == nil { // let go: made again at this height
		return
	}
	if windowOpen(h.panel) && h.glidePanel(height, g) {
		return
	}
	h.glides.Add(1)
	h.panel.SetSize(h.panelW(), height) // a flyout: placed by the icon again
}

// zoom is the text size the pages are drawn at, as a factor.
func (h *host) zoom() float64 { return zoomOf(int(h.textSize.Load())) }

// panelW is the panel's width, in points, at the text size.
func (h *host) panelW() int { return zoomed(panelWidth, h.zoom()) }

// panelRoom is how tall the panel can be on its screen: the work area,
// less the gap under the menu bar (or over the taskbar) and a little air.
func (h *host) panelRoom() int {
	if h.hyprRoom > 0 { // under Hyprland's bar, down to the work area's foot
		return h.hyprRoom
	}
	if _, rh := screenRoom(h.panel); rh > 0 {
		return rh - 16
	}
	return 0
}

// screenRoom is the work area of the screen w is on, 0s when unknown.
func screenRoom(w *mygo.Window) (int, int) {
	if w == nil {
		return 0, 0
	}
	a := mygo.Screen.DisplayMatching(w.Bounds()).WorkArea
	return a.Width, a.Height
}

// SetTextSize zooms both pages to percent, the Settings page's choice or a
// Ctrl/Cmd +, − or 0 in either window. The panel is resized at once, to
// the height its page last asked for; the main window is kept no smaller
// than the page's least, larger if it has to be.
func (h *host) SetTextSize(percent int) {
	if int(h.textSize.Swap(int64(percent))) == percent {
		return
	}
	h.whenReady(h.applyZoom)
}

// applyZoom puts the text size on the windows as they are now; one
// lightweight mode let go is made again at it.
func (h *host) applyZoom() { mygo.RunOnMain(h.zoomWindows) }

func (h *host) zoomWindows() {
	z := h.zoom()
	if h.main != nil {
		h.main.Page().SetZoomFactor(z)
		sw, sh := screenRoom(h.main)
		h.main.SetMinimumSize(windowMin(z, sw, sh))
	}
	page := h.panelPage
	if page == 0 {
		page = panelStart
	}
	w, ht := panelFrame(page, z, h.panelRoom())
	h.glides.Add(1)
	h.panelHeight = ht
	if h.panel == nil {
		return
	}
	h.panel.Page().SetZoomFactor(z)
	h.panel.SetSize(w, ht)
}

// panelStart is the panel's height before its page first asks for one.
const panelStart = 520

// Run starts the desktop app: a menu bar icon whose click drops down a compact
// panel, plus a regular window for when you want it to stay around.
// showMain opens the window immediately; otherwise only the tray icon appears.
// link is a magpie:// link the app was started with, to confirm and import.
func Run(version string, showMain bool, link string) error {
	Version = version
	webkitDefaults()
	// `make dev` runs the backend on its own, so a Go change restarts only
	// that, behind windows that stay up.
	if devRole() == "backend" {
		return devBackend(func(w Windows) http.Handler { return Handler(w, startBackend()) })
	}
	// After an update off the Mac, the old process starts this one and then
	// quits; let it go before looking for the gateway.
	update.AwaitPredecessor()
	// the Mac's second launch hands over to the magpie already running
	if runningAlready(showMain || OpenPanel, link) {
		return nil
	}
	// Windows and Linux start a new process for a magpie:// link (or a
	// second launch); it hands its arguments to the running one and quits.
	if runtime.GOOS != "darwin" && !lockInstance() {
		return nil
	}
	go func() {
		if err := registerScheme(); err != nil {
			log.Println("magpie:// links:", err)
		}
		// Windows has no installer to put magpie in the Start menu
		shortcut.Ensure()
		// Open at login as this version writes it (the Mac's, so a restart
		// to update from a magpie opened at login comes back)
		if err := autostart.Refresh(); err != nil {
			log.Println("open at login:", err)
		}
	}()
	// MAGPIE_THEME=light|dark forces the palette; handy for screenshots.
	theme := ""
	if t := os.Getenv("MAGPIE_THEME"); t != "" {
		theme = "&theme=" + t
	}
	h := &host{query: theme, ready: make(chan struct{})}
	// the pages open at the saved text size (the Page options' ZoomFactor)
	h.textSize.Store(int64(settings.Load().TextSize))
	go stats.Run(version, "app")
	handler := devShell(h)
	if handler == nil {
		handler = Handler(h, startBackend())
	}
	mygo.App.SetName("magpie")
	mygo.App.SetVersion(version)
	// the pages are magpie's own handler's, at mygo://localhost/
	// (http://mygo.localhost/ on Windows)
	if err := mygo.Protocol.Handle("mygo", handler); err != nil {
		return err
	}
	mygo.App.SetActivationPolicy(dockPolicy(settings.Load().Dock))
	// the tray stays when the windows are closed, or let go by lightweight mode
	mygo.App.OnWindowAllClosed(func() {})
	// mygo closes every window before it quits; one only hidden would stop it
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { h.quitting.Store(true) })
	// A version downloaded but not restarted into is installed on the
	// way out, so the next launch is the new one. The CLIs still being
	// asked something end here. Account failover magpie wrote into Codex
	// goes with the gateway this process serves: Codex left pointed at it
	// would retry till magpie is open again (Leon920717 on X). The next
	// start writes it again (Sync).
	mygo.App.OnQuit(func() {
		proc.EndProbes()
		if served.Load() != nil {
			if err := agent.EndCodexFailover(); err != nil {
				log.Println("codex failover:", err)
			}
		}
		updates.install(false)
	})
	mygo.App.OnSecondInstance(func(args []string, _ string) { h.secondInstance(args) })
	// The Mac hands a magpie:// link to the running app itself, the one it
	// started with too.
	if runtime.GOOS == "darwin" {
		var skip sync.Once
		mygo.App.OnOpenURL(func(u string) {
			u = ImportLink([]string{u})
			dup := false
			if u == link {
				skip.Do(func() { dup = true })
			}
			if u != "" && !dup {
				h.Import(u)
			}
		})
	}
	// The Dock icon opens the window, or goes to it where it is open.
	mygo.App.OnActivate(func(bool) { h.reopenMain() })
	onDock = func(s settings.Settings) { h.dock(s, h.MainShown()) }
	mygo.App.OnReady(func() { h.start(version, theme) })

	if showMain {
		h.whenReady(func() { h.ShowMain(argView(OpenView)) })
	}
	if OpenPanel {
		h.whenReady(func() { later(h.togglePanel) })
	}
	if link != "" {
		h.Import(link)
	}
	// GTK 3 with no font DPI set would lay every page out at a negative
	// width (#1371); give it one before the first webview is made.
	fontDPI()
	return mygo.App.Run()
}

// start makes the windows and the tray once the app has launched; on the
// main thread.
func (h *host) start(version, theme string) {
	if Started != nil {
		Started()
	}
	if icon := appIconFor(); icon != nil {
		_ = mygo.App.Dock.SetIcon(icon)
	}
	dockOnFullscreen()
	watchHide()
	// Portable, WebView2 keeps its profile in data\webview2: left to mygo it
	// is %LOCALAPPDATA%\magpie\WebView2, a folder a portable magpie must not
	// leave behind (#508). WebView2 reads the variable as the first window
	// makes its environment; it isn't left for what magpie runs.
	if dir := appdir.WebView(); dir != "" && runtime.GOOS == "windows" {
		os.Setenv("WEBVIEW2_USER_DATA_FOLDER", dir)
		defer os.Unsetenv("WEBVIEW2_USER_DATA_FOLDER")
	}
	h.menu = h.trayMenu(version)
	// Omarchy's bar shows magpie once: as its widget (Settings → Bar
	// icon) or as the tray's item, not both
	onBarIcon = h.setTrayInBar
	h.placeTray(barIcon(h)["on"])
	h.panel = h.makePanel()
	h.main = h.makeMain("/?" + theme)
	onFonts = h.fontsChanged

	updates.start()
	news.start()
	// the library written into the agents again, once: one installed or
	// updated since (or an edit by hand) gets it without a visit to the page
	go func() {
		if res, err := library.Sync(); err != nil {
			log.Println("library sync:", err)
		} else {
			for _, p := range res.Problems {
				log.Println("library sync:", p.Agent, p.What, p.Error)
			}
		}
	}()
	h.watchTrayUsage()
	go h.lighten()
	h.watchAlerts()
	h.readyOnce.Do(func() { close(h.ready) })
}

// trayMenu is the tray's menu, in the page's language, relabelled when
// that changes (#301).
func (h *host) trayMenu(version string) *mygo.Menu {
	labels := trayMenuLabels(trayLang(settings.Load().Lang, systemLang), version, "")
	open := &mygo.MenuItem{Label: labels.open, Click: func(*mygo.MenuItem, *mygo.Window) { go h.ShowMain("") }}
	ver := &mygo.MenuItem{Label: labels.version, Disabled: true}
	restart := &mygo.MenuItem{Label: labels.restart, Hidden: true, Click: func(*mygo.MenuItem, *mygo.Window) {
		// the window comes back if it was open; the tray alone if not
		now := func() bool {
			if restartToUpdate(false, h.MainShown(), "") {
				mygo.App.Quit()
				return true
			}
			return false
		}
		// with the gateway busy the first click waits for it to be idle,
		// and the item then restarts at once (#577)
		go func() {
			if waiting, _ := updates.waitingFor(); !waiting && !updates.idle() {
				updates.waitIdle(now)
				return
			}
			updates.cancelWait()
			now()
		}()
	}}
	quit := &mygo.MenuItem{Label: labels.quit, Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }}
	var ready string // the version waiting for a restart; on the main thread
	relabel := func() {
		l := trayMenuLabels(trayLang(settings.Load().Lang, systemLang), version, ready)
		if waiting, b := updates.waitingFor(); waiting && ready != "" {
			l.restart = trayRestartNow(trayLang(settings.Load().Lang, systemLang), b)
		}
		open.SetLabel(l.open)
		ver.SetLabel(l.version)
		restart.SetLabel(l.restart)
		quit.SetLabel(l.quit)
	}
	onLang = func() { mygo.RunOnMain(relabel) }
	updates.onReady = func(v string) {
		mygo.RunOnMain(func() {
			ready = v
			restart.SetVisible(true)
			relabel()
		})
	}
	updates.onWait = func() { mygo.RunOnMain(relabel) }
	return mygo.NewMenu([]*mygo.MenuItem{open, mygo.Separator(), ver, restart, quit})
}

// makeTray puts magpie's icon in the tray; on the main thread. A click
// opens the quick panel by it, or the main window if the user would rather
// (Settings → Tray icon), and a right-click the menu. Linux's tray tells
// nothing of clicks: there the icon has the menu only.
func (h *host) makeTray() {
	icon, template := appIcon, false
	if runtime.GOOS == "darwin" {
		icon, template = trayIcon, true
	}
	t, err := mygo.NewTray(mygo.TrayOptions{Icon: icon, IconIsTemplate: template, ToolTip: "magpie"})
	if err != nil {
		log.Println("tray:", err)
		return
	}
	h.tray = t
	if runtime.GOOS == "linux" {
		t.SetMenu(h.menu)
		return
	}
	t.OnClick(func() {
		if cmdClick() {
			return // Command-drag moves the icon; the system handles it
		}
		if runtime.GOOS == "darwin" {
			go h.flap()
		}
		if settings.Load().Tray == "window" {
			go h.ShowMain("")
			return
		}
		h.togglePanelNow()
	})
	t.OnRightClick(func() {
		if h.panel != nil {
			h.panel.Hide()
		}
		// the menu for this popup only, so that clicks keep opening the panel
		t.SetMenu(h.menu)
		defer t.SetMenu(nil)
		t.PopUpMenu()
	})
}

// placeTray has the tray's icon leave the bar's tray while magpie's icon is
// in Omarchy's bar as its widget, and come back once it isn't; on the main
// thread. AppIndicator has no passive state, so the icon is taken away.
func (h *host) placeTray(inBar bool) {
	switch {
	case inBar && h.tray != nil:
		h.tray.Destroy()
		h.tray = nil
	case !inBar && h.tray == nil:
		h.makeTray()
	}
}

// setTrayInBar is placeTray from anywhere, once the app has started.
func (h *host) setTrayInBar(inBar bool) {
	h.whenReady(func() { mygo.RunOnMain(func() { h.placeTray(inBar) }) })
}

// lockInstance makes this the one magpie on its config, or hands this
// launch's arguments to the one running and says so. The lock covers the
// config dir, so a sandboxed HOME runs on its own, but not the executable:
// two copies of magpie on one config (one autostarted from where it was
// first run, another from where it was put later) would share the
// gateway's port and put two icons in the tray. mygo names the lock after
// the app as it is called when the lock is taken.
func lockInstance() bool {
	sum := sha256.Sum256([]byte(settings.Dir()))
	mygo.App.SetName("magpie-" + hex.EncodeToString(sum[:6]))
	defer mygo.App.SetName("magpie")
	return mygo.App.RequestSingleInstanceLock()
}

// secondInstance answers a second launch's arguments.
func (h *host) secondInstance(args []string) {
	switch {
	case ImportLink(args) != "":
		h.Import(ImportLink(args))
	case len(args) == 1 && args[0] == "tray":
	case len(args) == 1 && args[0] == "panel":
		h.whenReady(func() { later(h.togglePanel) })
	case len(args) == 2 && (args[0] == "gui" || args[0] == "app"):
		h.whenReady(func() { h.ShowMain(argView(args[1])) })
	default:
		h.whenReady(func() { h.ShowMain("") })
	}
}

// flap plays trayFlap on the tray icon, a frame every 30ms as they were
// drawn — the .9s of the header logo's flap — and ends on the still bird.
func (h *host) flap() {
	if !h.flapping.CompareAndSwap(false, true) {
		return
	}
	defer h.flapping.Store(false)
	names, _ := fs.Glob(trayFlap, "trayflap/*.png")
	for _, n := range names {
		b, err := trayFlap.ReadFile(n)
		if err != nil {
			break
		}
		h.setBird(b)
		time.Sleep(30 * time.Millisecond)
	}
	h.setBird(trayIcon)
}

// setBird sets a frame of the bird: in the menu bar's image while it shows
// the usage cards (trayImageFrame), else as the tray's icon.
func (h *host) setBird(b []byte) {
	if trayImageFrame(b) {
		return
	}
	mygo.RunOnMain(func() {
		if h.tray != nil {
			_ = h.tray.SetIcon(b, true)
		}
	})
}

// OpenPanel has Run open the quick panel once it starts, as `magpie panel`
// does; a second `magpie panel` toggles it in the running one. Omarchy's bar
// icon runs it (see omarchy.AddWidget).
var OpenPanel bool

// OpenView is the tab the window opens on, as `magpie gui settings` asks:
// a restart to update comes back where it was asked for.
var OpenView string

// Started is called once the app has started, by when mygo handles SIGINT
// and SIGTERM itself (it starts listening as it runs, before the app is
// said to be ready); until then a signal is magpie's to handle.
var Started func()

// togglePanel opens the quick panel by the tray icon, or closes it.
func (h *host) togglePanel() { mygo.RunOnMain(h.togglePanelNow) }

func (h *host) togglePanelNow() {
	p := h.panelWin()
	if omarchy.Hyprland() {
		if p.IsVisible() {
			h.hidePanel()
			return
		}
		h.placePanel()
	}
	// niri places it as it opens (see omarchy.NiriPanel)
	if omarchy.Niri() && !p.IsVisible() {
		h.niriWatch.Do(func() { go h.niri.Watch(panelTitle) })
		h.niri.Opening(nil)
	}
	// Open is windowOpen: shown, covered or not. On the Mac IsVisible is the
	// window's occlusion: under a window over the whole screen (a
	// remote-desktop server's, above every app's) the open panel read as
	// closed, and the icon showed it again instead of closing it (#1661).
	if windowOpen(p) {
		p.Hide()
		return
	}
	if runtime.GOOS != "linux" && h.tray != nil {
		// a flyout is placed by the icon as it is sized, not as it shows
		w, ht := p.Size()
		p.SetSize(w, ht)
	}
	p.Show()
}

// placePanel puts the panel, about to open, under Hyprland's bar at the
// click, the way Omarchy's own drop-downs sit: a Wayland window can't place
// itself, so Hyprland is given a rule for it (see omarchy.PlacePanel).
func (h *host) placePanel() {
	x, y, room, ok := omarchy.PanelAt(h.panelW())
	if !ok {
		return
	}
	if err := omarchy.PlacePanel(panelTitle, x, y); err != nil {
		log.Println("hyprland:", err)
		return
	}
	h.hyprRoom = room
	h.clicks.Do(func() {
		go omarchy.WatchClicks(func() {
			switch {
			case !onMain(func() bool { return h.panel != nil && h.panel.IsVisible() }): // Escape closed it
				omarchy.StopClicks()
			case omarchy.ClickedOutside(panelTitle):
				later(h.hidePanel)
			}
		})
	})
	if err := omarchy.ReportClicks(); err != nil {
		log.Println("hyprland:", err)
	}
	if _, ht := h.panel.Size(); ht > room && room >= panelMin {
		h.panelHeight = room
		h.panel.SetSize(h.panelW(), room)
	}
}

// hidePanel closes the panel on Hyprland, where it has no focus-lost to
// close on (see omarchy.ReportClicks).
func (h *host) hidePanel() {
	if h.panel != nil {
		h.panel.Hide()
	}
	go omarchy.StopClicks()
}
