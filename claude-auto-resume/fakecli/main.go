// Command fakecli is a tiny stand-in for an AI CLI that hits a usage limit. It
// drives claude-auto-resume's integration test and doubles as a live demo you
// can wrap by hand. State is persisted in the file given as the first argument
// (or $CAR_FAKE_STATE) so a re-spawn behaves like a resumed session.
//
//	exec demo:  claude-auto-resume --no-pty -- fakecli /tmp/car-demo
//	wrap demo:  claude-auto-resume -- fakecli -mode wrap /tmp/car-demo
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	mode := flag.String("mode", "exec", "exec|wrap")
	wait := flag.String("wait", "1s", "reset hint to print in the limit message")
	flag.Parse()

	state := os.Getenv("CAR_FAKE_STATE")
	if a := flag.Arg(0); a != "" {
		state = a
	}

	n := readCount(state)
	writeCount(state, n+1)

	if n > 0 {
		// We were re-spawned (exec mode resume): the "session" continues.
		fmt.Println("RESUMED-OK: continuing after limit reset")
		return
	}

	fmt.Println("fakecli: starting work...")
	if *mode == "wrap" {
		fmt.Print("\x1b[?1049h") // enter alt screen so auto-resume treats us as a TUI
	}
	fmt.Printf("Rate limit reached. Please try again in %s.\n", *wait)

	if *mode == "wrap" {
		// Stay alive like a TUI and wait for the injected resume keystrokes.
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			fmt.Print("\x1b[?1049l")
			fmt.Printf("RESUMED-OK: got %q\n", strings.TrimSpace(sc.Text()))
		}
		return
	}
	// exec mode: exit non-zero so the supervisor re-spawns us.
	os.Exit(1)
}

func readCount(path string) int {
	if path == "" {
		return 0
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func writeCount(path string, n int) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(n)), 0o644)
}
