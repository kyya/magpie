//go:build !darwin && !nogui

package gui

import "github.com/egoist/mygo"

// Spaces are the Mac's: elsewhere a window is shown where it was.
func setMovesToActiveSpace(*mygo.Window, bool) {}

func windowOpen(w *mygo.Window) bool { return w.IsVisible() && !w.IsMinimized() }

// Elsewhere a window is visible covered or minimised alike.
func windowUp(w *mygo.Window) bool { return w.IsVisible() }

func activateApp() {}

func watchHide() {}
