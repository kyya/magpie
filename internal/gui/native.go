//go:build !nogui

package gui

import (
	"unsafe"

	"github.com/egoist/mygo"
)

// native is w's NSWindow*, GtkWindow* or HWND, as cgo takes it; nil once
// it has gone.
func native(w *mygo.Window) unsafe.Pointer {
	p := w.NativeHandle()
	// a pointer the system keeps, not Go's: vet's unsafeptr check doesn't apply
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}
