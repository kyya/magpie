//go:build darwin && cgo && !nogui

package gui

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

// A separate process runs AppKit on its main thread. It uses an isolated
// config and a minimal page, without starting magpie's backend or status item.
func TestTrayCellClickReleasedPanel(t *testing.T) {
	out, err := runAppKit(t, 20*time.Second, []string{"MAGPIE_TEST_TRAY_PANEL=1"})
	if err != nil || !strings.Contains(string(out), "tray quota: recreated and focused twice") {
		t.Fatalf("released panel: %v\n%s", err, out)
	}
}

func init() {
	if os.Getenv("MAGPIE_TEST_TRAY_PANEL") != "1" {
		return
	}
	seen := make(chan string, 2)
	page := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/seen" {
			seen <- r.URL.Query().Get("id")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<script>function panelQuotaFocus(id) { fetch('/seen?id=' + encodeURIComponent(id)); }</script>`)
	}
	// no tray: the panel is a window of its own, not a flyout by an icon
	h := &host{}
	runApp("magpie tray regression", mygo.ActivationPolicyProhibited, page, func() {
		mygo.RunOnMain(func() { h.trayCellClick("codex|first@test") })
		if id := <-seen; id != "codex|first@test" {
			panic("wrong initial target: " + id)
		}
		// Open is windowOpen, shown whether covered or not: under a window
		// over the whole screen (a remote-desktop server's) the panel just
		// opened read as closed by its occlusion, and the second click
		// showed it again (#1661).
		open := func(w *mygo.Window) func() bool { return func() bool { return windowOpen(w) } }
		var first *mygo.Window
		mygo.RunOnMain(func() {
			first = h.panel
			if !windowOpen(first) {
				panic("initial quota click did not open the panel")
			}
			h.trayCellClick("codex|first@test") // the second click closes it
		})
		deadline := time.Now().Add(2 * time.Second)
		for onMain(open(first)) {
			if time.Now().After(deadline) {
				panic("second quota click did not close the panel")
			}
			time.Sleep(10 * time.Millisecond)
		}
		mygo.RunOnMain(func() {
			// Same release state as lighten: the closed panel no longer exists.
			first.Destroy()
			h.panel = nil
			h.trayCellClick("codex|second@test")
			if h.panel == nil || h.panel == first {
				panic("panel was not recreated")
			}
		})
		if id := <-seen; id != "codex|second@test" {
			panic("wrong recreated target: " + id)
		}
		fmt.Println("tray quota: recreated and focused twice")
	})
}
