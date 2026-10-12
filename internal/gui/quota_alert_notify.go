//go:build !nogui

package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// notifier is the system's notifications. The Mac asks the user the first
// time one is shown, and keeps the answer.
type notifier struct {
	h *host
	// refused is the Mac's answer when a notification was shown this run:
	// before that, not yet allowed and turned off read the same
	refused atomic.Bool
}

// problem is notifyProblem: why a notification wouldn't be seen.
func (n *notifier) problem() string {
	select {
	case <-n.h.ready:
	default:
		return "" // the app isn't running yet
	}
	switch {
	case !mygo.NotificationsSupported():
		return "unavailable"
	case n.refused.Load():
		return "denied"
	}
	return ""
}

// turnNotice is the id a turn's end is notified under, an alert's
// "magpie-alert-"
const turnNotice = "magpie-turn-"

func (n *notifier) send(title, body string) { n.sendAs("magpie-alert-", title, body) }

func (n *notifier) sendAs(kind, title, body string) {
	if !mygo.NotificationsSupported() {
		return
	}
	id := fmt.Sprintf("%s%d", kind, time.Now().UnixNano())
	err := mygo.NewNotification(mygo.NotificationOptions{ID: id, Title: title, Body: body}).Show()
	n.refused.Store(errors.Is(err, mygo.ErrNotificationsDenied))
	if err != nil {
		log.Println("notifications:", err)
	}
}

// watchAlerts tells the user, with a notification, of a window used past
// the share or a balance fallen to the amount set in Settings (#368).
func (h *host) watchAlerts() {
	n := &notifier{h: h}
	// a click on an alert opens the Usage page; on a turn's end, where
	// the user goes is their agent, not magpie
	mygo.App.OnNotificationClick(func(id string) {
		if strings.HasPrefix(id, "magpie-") && !strings.HasPrefix(id, turnNotice) {
			h.whenReady(func() { h.ShowMain("usage") })
		}
	})
	wake := make(chan struct{}, 1)
	onAlerts = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	notifyProblem = n.problem
	go func() {
		<-h.ready
		// an agent's turn done, past the length set (itieniu on X)
		gateway.TurnDone = func(t gateway.TurnEnd) {
			title, body := turnDoneText(trayLang(settings.Load().Lang, systemLang), agentName(t.Agent), t)
			n.sendAs(turnNotice, title, body)
		}
		provider.WatchQuotas(context.Background(), wake, func(a provider.QuotaAlert) {
			s := settings.Load()
			title, body := alertText(trayLang(s.Lang, systemLang), a, s.BalanceAlert, s.QuotaLeft, time.Now())
			n.send(title, body)
		})
	}()
}
