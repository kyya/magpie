//go:build !nogui

package gui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
)

// #508: a portable magpie on Windows still made %APPDATA%\magpie.exe,
// WebView2's profile at the webview's default. Portable, start puts it in
// data\webview2; installed, it is left at the default (and what is there).
func TestWindowsOptionsWebViewData(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	t.Cleanup(func() { appdir.UseExecutable("") })
	r, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	appdir.UseExecutable("")
	if d := appdir.WebView(); d != "" {
		t.Fatalf("installed: %q, want WebView2's default folder", d)
	}

	exe := filepath.Join(r, "Magpie", "magpie.exe")
	if err := os.MkdirAll(filepath.Join(r, "Magpie", "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	appdir.UseExecutable(exe)
	if d, want := appdir.WebView(), filepath.Join(r, "Magpie", "data", "webview2"); d != want {
		t.Fatalf("portable: WebView = %q, want %q", d, want)
	}
}
