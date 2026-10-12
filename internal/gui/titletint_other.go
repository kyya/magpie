//go:build !windows && !nogui

package gui

import "github.com/egoist/mygo"

// TintTitleBar: the Mac's title bar is hidden in the page, and Linux's is
// the window manager's.
func (h *host) TintTitleBar([4]uint8, bool) bool { return false }

// windowChrome: the window's own background until its page paints, as the
// webview has it.
func windowChrome(string) string { return "" }

func tintNew(*mygo.Window, string) {}
