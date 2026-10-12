//go:build !windows && !nogui

package gui

import "github.com/egoist/mygo"

func openFolder(path string) error { return mygo.Shell.OpenPath(path) }
