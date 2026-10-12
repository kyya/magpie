//go:build (!linux || !cgo || !gtk3) && !nogui

package gui

import "github.com/egoist/mygo"

// plainTitlebar: macOS insets the traffic lights into the page's header
// (TitleBarHiddenInset) and Windows keeps its own title bar.
func plainTitlebar(*mygo.Window) {}

func ownFrame(*mygo.Window) {}
