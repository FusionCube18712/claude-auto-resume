//go:build !windows

package supervisor

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// startResize keeps the child's PTY sized to the controlling terminal, reacting
// to SIGWINCH. It returns a stop function.
func startResize(st stream) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)

	apply := func() {
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			st.Resize(w, h)
		}
	}
	apply() // size once up front

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ch:
				apply()
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
