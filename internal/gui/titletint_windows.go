//go:build windows && !nogui

package gui

import (
	"fmt"
	"unsafe"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// TintTitleBar paints the window's title bar the page's own colour, dark
// or light as the page is, so the bar and the page read as one surface as
// the Mac's hidden title bar does; Windows' grey bar sat above the page as
// a strip of its own, and stayed light over a page set to dark.
func (h *host) TintTitleBar(c [4]uint8, dark bool) bool {
	if !customTitleBars() {
		return false
	}
	mygo.RunOnMain(func() {
		w := h.main
		if w == nil { // let go (lightweight mode); its page tints it when made again
			return
		}
		tintTitle(w, c, dark)
		// what shows past the page's edge while the window is resized
		_ = w.SetBackgroundColor(fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]))
	})
	return true
}

// tintTitle sets the title bar's theme and colour (DWM's caption colour,
// Windows 11's; Windows 10 takes the theme alone).
func tintTitle(w *mygo.Window, c [4]uint8, dark bool) {
	hwnd := windows.HWND(w.NativeHandle())
	if hwnd == 0 {
		return
	}
	var on uint32
	if dark {
		on = 1
	}
	_ = windows.DwmSetWindowAttribute(hwnd, windows.DWMWA_USE_IMMERSIVE_DARK_MODE, unsafe.Pointer(&on), 4)
	colour := uint32(c[0]) | uint32(c[1])<<8 | uint32(c[2])<<16
	_ = windows.DwmSetWindowAttribute(hwnd, windows.DWMWA_CAPTION_COLOR, unsafe.Pointer(&colour), 4)
}

// customTitleBars says whether DWM takes a title bar's theme: Windows 10
// 1809 on.
func customTitleBars() bool {
	v := windows.RtlGetVersion()
	return v.MajorVersion > 10 || v.MajorVersion == 10 && v.BuildNumber >= 17763
}

// windowChrome is the window's background before its page has said what
// colour it is: the page's own --bg for the theme it will open in.
func windowChrome(theme string) string {
	if chromeDark(theme) {
		return "#1a1a1e"
	}
	return "#f4f4f6"
}

// tintNew gives a window just made the title bar of the theme its page will
// open in, from the first frame.
func tintNew(w *mygo.Window, theme string) {
	if !customTitleBars() {
		return
	}
	c, dark := [4]uint8{0xf4, 0xf4, 0xf6, 0xff}, chromeDark(theme)
	if dark {
		c = [4]uint8{0x1a, 0x1a, 0x1e, 0xff}
	}
	tintTitle(w, c, dark)
}

func chromeDark(theme string) bool {
	return theme == "dark" || theme != "light" && systemDark()
}

// systemDark says whether Windows has apps in dark mode.
func systemDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	light, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && light == 0
}
