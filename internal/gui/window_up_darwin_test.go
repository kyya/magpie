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

// The main window open under another window is open (#1381). Lightweight
// mode took the occlusion state for shown, so the window the user had
// switched away from with Command-Tab was let go as closed, and switching
// back found it blank or gone. Hidden with magpie (Command-H) or minimised,
// it is open too; closed by magpie, it isn't.
func TestCoveredWindowStaysUp(t *testing.T) {
	out, err := runAppKit(t, 30*time.Second, []string{"MAGPIE_TEST_WINDOW_UP=1"})
	if err != nil || !strings.Contains(string(out), "window up: ok") {
		t.Fatalf("window up: %v\n%s", err, out)
	}
}

func init() {
	if os.Getenv("MAGPIE_TEST_WINDOW_UP") != "1" {
		return
	}
	h := &host{}
	fail := func(format string, a ...any) {
		fmt.Printf("window up: "+format+"\n", a...)
		os.Exit(1)
	}
	waitFor := func(what string, ok func() bool) {
		for deadline := time.Now().Add(5 * time.Second); !onMain(ok); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				fail("timed out waiting until %s", what)
			}
		}
	}
	// kept: what the Dock, a restart and lightweight mode go by
	kept := func() bool { return h.mainShown() && h.lightShown(h.main) }
	expect := func(how string, want bool) {
		if got := onMain(kept); got != want {
			fail("%s: the window counts as up=%v, want %v", how, got, want)
		}
	}
	watchHide()
	runApp("magpie window up regression", mygo.ActivationPolicyAccessory, plainPage, func() {
		mygo.RunOnMain(func() { h.openMain("") })
		waitFor("the window is open", func() bool { return windowOpen(h.main) })
		expect("open", true)

		// covered by another window, as by the app Command-Tab went to
		cover := onMain(func() *mygo.Window {
			x, y := h.main.Position()
			wd, ht := h.main.Size()
			c := mygo.NewWindow(mygo.WindowOptions{
				URL: "/", Width: wd + 200, Height: ht + 200,
				Frameless: true, AlwaysOnTop: true, BackgroundColor: "#008080",
			})
			c.SetPosition(x-100, y-100)
			c.Show()
			return c
		})
		// shortcut: mygo has no occlusion state to wait on; a fixed wait for the cover to draw
		time.Sleep(500 * time.Millisecond)
		expect("covered", true)
		mygo.RunOnMain(cover.Destroy)

		// hidden with magpie, as by Command-H. Opening asked the Mac to
		// activate magpie (Focus), and on a loaded CI machine that
		// activation can land after the hide, which unhides magpie and
		// shows the window again: the hide and the check are one step on
		// the main thread, and a hide undone that way is done again.
		var hiddenUp bool
		waitFor("magpie is hidden", func() bool {
			if windowOpen(h.main) {
				mygo.App.Hide()
			}
			hiddenUp = kept()
			return !windowOpen(h.main)
		})
		if !hiddenUp {
			fail("hidden with magpie: the window counts as up=false, want true")
		}
		mygo.RunOnMain(mygo.App.Show)
		waitFor("magpie is shown again", func() bool { return windowOpen(h.main) })

		// minimised
		mygo.RunOnMain(func() { h.main.Minimize() })
		waitFor("the window is minimised", func() bool { return h.main.IsMinimized() })
		expect("minimised", true)
		mygo.RunOnMain(func() { h.main.Restore() })
		waitFor("the window is back", func() bool { return windowOpen(h.main) })

		// closed by magpie
		h.hideMain()
		waitFor("the window is closed", func() bool { return !windowOpen(h.main) })
		expect("closed", false)

		// closed, then magpie hidden: still closed
		mygo.RunOnMain(mygo.App.Hide)
		time.Sleep(300 * time.Millisecond)
		expect("closed, magpie hidden", false)
		mygo.RunOnMain(mygo.App.Show)

		fmt.Println("window up: ok")
	})
}
