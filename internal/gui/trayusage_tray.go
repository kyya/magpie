//go:build !nogui

package gui

import (
	"context"
	"log"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// watchTrayUsage keeps the tray's cards up to date, and plain "magpie" in
// the tooltip while there are none.
func (h *host) watchTrayUsage() {
	wake := make(chan struct{}, 1)
	onTrayUsage = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	// a card read while stale is refreshed behind it: read it again once
	// that lands, not a tick later, so the menu bar says what the panel does
	provider.OnSubscriptionUsage(onTrayUsage)
	// another account answering, or the agent switched to another: a card
	// following the account in use reads it at once (#1516)
	provider.OnServedMoved(onTrayUsage)
	// Schedule native cell clicks on the application thread after startup.
	onTrayCellClick = func(id string) {
		h.whenReady(func() { later(func() { h.trayCellClick(id) }) })
	}
	go func() {
		<-h.ready // the tray is made once the app runs
		shown, drawn := "", false
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			s := settings.Load()
			cells, label, tip := trayUsageView(trayUsageCards(ctx, s.TrayUsages), time.Now(), s.QuotaLeft)
			cancel()
			if s.TrayNoLogos {
				cells = trayPlain(cells)
			}
			if tip == "" {
				tip = "magpie"
			}
			if runtime.GOOS != "darwin" {
				cells = nil
			} else if label != "" {
				// the menu bar sets the text hard against the icon
				label = " " + label
			}
			// what is shown, to set it again only when it changes
			now := label + "\x00" + tip
			for _, c := range cells {
				now += "\x00" + c.Letter + strings.Join(c.Rows, "\x01")
				if c.Plain {
					now += "\x02"
				}
			}
			if now != shown {
				shown = now
				// the Mac's cells as an image, the text where it can't be
				mygo.RunOnMain(func() {
					if h.tray == nil {
						shown = "" // in Omarchy's bar: again once it is back
						return
					}
					if len(cells) > 0 {
						h.tray.SetTitle("")
					} else {
						if drawn {
							drawn = false
							trayImageHide()
							_ = h.tray.SetIcon(trayIcon, true)
						}
						trayTitle(h.tray, label)
					}
					h.tray.SetToolTip(tip)
				})
			}
			// Reapply even unchanged cells: a system menu-bar rebuild can
			// replace the composed image with the tray's own bird icon.
			if len(cells) > 0 {
				bird := trayIcon
				if s.TrayNoBird {
					bird = nil // the cards alone
				}
				if trayImageShow(cells, bird) {
					drawn = true
				} else {
					mygo.RunOnMain(func() {
						if h.tray != nil {
							trayTitle(h.tray, label)
						}
					})
					shown = "" // and try again next time
				}
			}
			// the first answer can take a while; look again soon after it
			next := trayUsageEvery()
			if label == "" && len(s.TrayUsages) > 0 {
				next = 20 * time.Second
			}
			select {
			case <-wake:
			case <-time.After(next):
			}
		}
	}()
}

// trayCellClick opens the allowance represented by the clicked image cell.
// A visible panel closes on the next click, as clicking the bird does.
func (h *host) trayCellClick(id string) {
	if id == "" {
		return
	}
	if settings.Load().Tray == "window" {
		h.ShowMain(quotaView(id))
		return
	}
	w := h.panelWin()
	if !windowOpen(w) {
		evalLater(w, panelQuotaJS(id))
	}
	h.togglePanelNow()
}

// trayTitle sets the text by the icon: the Mac's menu bar shows it, and
// AppIndicator would put it beside the icon on Linux, where the tooltip
// says it instead.
func trayTitle(t *mygo.Tray, label string) {
	if runtime.GOOS == "darwin" {
		t.SetTitle(label)
	}
}

// evalLater runs js in w's page once it has loaded, without waiting for
// it: Eval waits for the page, which on the main thread would wait for
// itself. A window just made (or made again after lightweight mode let it
// go) is still on its blank page, and js run there is lost.
func evalLater(w *mygo.Window, js string) {
	go func() {
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			ready, err := mygo.EvalAs[bool](w.Page(), `document.readyState === "complete" && location.href !== "about:blank"`)
			if err != nil || ready {
				break
			}
			if time.Now().After(deadline) {
				log.Println("page: never loaded")
				return
			}
		}
		if _, err := w.Page().Eval(js); err != nil {
			log.Println("page:", err)
		}
	}()
}
