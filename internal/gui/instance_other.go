//go:build !darwin

package gui

// runningAlready is the Mac's; off it mygo's single instance lock hands a
// second launch over (lockInstance).
func runningAlready(showMain bool, link string) bool { return false }
