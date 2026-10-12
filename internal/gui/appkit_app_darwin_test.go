//go:build darwin && cgo && !nogui

package gui

import (
	"fmt"
	"net/http"
	"os"

	"github.com/egoist/mygo"
)

// plainPage is the page a regression process's windows load.
func plainPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<p>magpie</p>`)
}

// runApp runs the app in a process of runAppKit's, its pages served by page,
// and body on a goroutine once it has launched; the process exits when body
// returns.
func runApp(name string, policy mygo.ActivationPolicy, page http.HandlerFunc, body func()) {
	mygo.App.SetName(name)
	mygo.App.SetActivationPolicy(policy)
	if err := mygo.Protocol.Handle("mygo", page); err != nil {
		panic(err)
	}
	mygo.App.OnWindowAllClosed(func() {}) // a window closed doesn't end the test
	mygo.App.OnReady(func() {
		go func() {
			body()
			mygo.App.Quit()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}
