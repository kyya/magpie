//go:build darwin && cgo && !nogui

package gui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

// The system's close on the quick panel (KWin's title bar X on KDE, #1283;
// Alt+F4; a window manager's close key) reaches magpie as OnClose, as Close
// sends it. The panel is hidden and stays the tray's, so the icon opens it
// again; closed, the icon did nothing until a restart. A panel lightweight
// mode let go is destroyed.
func TestPanelCloseHidesIt(t *testing.T) {
	out, err := runAppKit(t, 30*time.Second, []string{"MAGPIE_TEST_PANEL_CLOSE=1"})
	if err != nil || !strings.Contains(string(out), "panel close: ok") {
		t.Fatalf("panel close: %v\n%s", err, out)
	}
}

func init() {
	if os.Getenv("MAGPIE_TEST_PANEL_CLOSE") != "1" {
		return
	}
	h := &host{}
	fail := func(format string, a ...any) {
		fmt.Printf("panel close: "+format+"\n", a...)
		os.Exit(1)
	}
	waitFor := func(what string, ok func() bool) {
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				fail("timed out waiting until %s", what)
			}
		}
	}
	// Shown is windowOpen, covered or not: under a window over the whole
	// screen (a remote-desktop server's) the shown panel read as hidden
	// by its occlusion (#1661).
	visible := func(w *mygo.Window) func() bool {
		return func() bool { return onMain(func() bool { return windowOpen(w) }) }
	}
	runApp("magpie panel close regression", mygo.ActivationPolicyProhibited, plainPage, func() {
		w := onMain(func() *mygo.Window {
			w := h.panelWin()
			w.Show()
			return w
		})
		waitFor("the panel shows", visible(w))

		w.Close() // the title bar's X
		waitFor("the closed panel is hidden", func() bool { return !visible(w)() })
		time.Sleep(300 * time.Millisecond)
		if w.IsDestroyed() {
			fail("the panel closed for good; the tray icon can't open it again")
		}
		if onMain(func() bool { return h.panel != w }) {
			fail("the panel was replaced")
		}
		mygo.RunOnMain(w.Show) // the icon clicked again
		waitFor("the panel shows again", visible(w))

		// lightweight mode lets it go (lighten): then it is gone
		mygo.RunOnMain(func() { w.Hide(); h.panel = nil; w.Destroy() })
		waitFor("the panel let go is gone", w.IsDestroyed)
		fmt.Println("panel close: ok")
	})
}
