//go:build windows

package supervisor

import (
	"os"
	"time"

	"golang.org/x/term"
)

// startResize keeps the child's ConPTY sized to the console. Windows has no
// SIGWINCH, so we poll the console size and resize on change. Best-effort.
func startResize(st stream) func() {
	done := make(chan struct{})
	go func() {
		lastW, lastH := -1, -1
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && (w != lastW || h != lastH) {
				st.Resize(w, h)
				lastW, lastH = w, h
			}
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done) }
}
