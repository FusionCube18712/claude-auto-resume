// Package detect scans a child process's terminal output for usage-limit
// messages. It strips ANSI escape sequences before matching (TUIs colour and
// reposition the text), keeps a rolling buffer so a message split across reads
// is still caught, and tracks whether the child is using the alternate-screen
// buffer (which tells the supervisor it's a full TUI and should be resumed by
// injecting keystrokes rather than re-spawning).
package detect

import (
	"regexp"
	"sync"
)

// ansiPattern matches the escape sequences a terminal app emits: CSI (colour,
// cursor moves, private modes like ?1049h), OSC (window titles), and the
// shorter two-byte escapes. Matched spans are removed before limit matching.
var ansiPattern = regexp.MustCompile(
	"\x1b\\[[0-9;?]*[ -/]*[@-~]" + // CSI ... final byte
		"|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)" + // OSC ... BEL or ST
		"|\x1b[@-Z\\\\-_]", // 2-byte escapes
)

// altEnter / altLeave match the private-mode sequences that switch in and out of
// the alternate screen buffer (1049 is standard; 1047/47 are legacy variants).
var (
	altEnter = regexp.MustCompile("\x1b\\[\\?(?:1049|1047|47)h")
	altLeave = regexp.MustCompile("\x1b\\[\\?(?:1049|1047|47)l")
)

// StripANSI removes terminal escape sequences, leaving the visible text.
func StripANSI(b []byte) []byte {
	return ansiPattern.ReplaceAll(b, nil)
}

// Detector accumulates stripped output and reports the first time any limit
// pattern matches. It latches after a hit until Reset is called, so the same
// stale message is not reported twice. It is safe for concurrent use: Feed runs
// on the output-reader goroutine while Reset/AltScreen are called from the
// supervisor goroutine.
type Detector struct {
	patterns  []*regexp.Regexp
	mu        sync.Mutex
	buf       []byte
	max       int
	armed     bool
	altScreen bool
}

// New returns a Detector matching any of patterns, keeping at most bufSize bytes
// of recent (stripped) output for matching.
func New(patterns []*regexp.Regexp, bufSize int) *Detector {
	if bufSize <= 0 {
		bufSize = 16 << 10
	}
	return &Detector{patterns: patterns, max: bufSize, armed: true}
}

// Feed consumes a chunk of raw output. It returns hit=true exactly once per
// limit occurrence (until Reset), along with the recent stripped context that
// contains the match — which is handed to resettime.Parse.
func (d *Detector) Feed(raw []byte) (hit bool, context string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if altEnter.Match(raw) {
		d.altScreen = true
	}
	if altLeave.Match(raw) {
		d.altScreen = false
	}

	d.buf = append(d.buf, StripANSI(raw)...)
	if len(d.buf) > d.max {
		d.buf = d.buf[len(d.buf)-d.max:]
	}
	if !d.armed {
		return false, ""
	}
	for _, p := range d.patterns {
		if p.Match(d.buf) {
			d.armed = false
			return true, string(d.buf)
		}
	}
	return false, ""
}

// AltScreen reports whether the child is currently in the alternate screen
// buffer (i.e. a full-screen TUI like Claude Code).
func (d *Detector) AltScreen() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.altScreen
}

// Reset re-arms the detector and clears the matched buffer so that output
// produced after a resume can trigger a fresh detection.
func (d *Detector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed = true
	d.buf = d.buf[:0]
}
