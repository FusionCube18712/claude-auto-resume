// Package ui renders supervisor events for a human: a one-line "limited"
// notice, an in-place live countdown, and resume/info lines. Everything is
// written to stderr so it never contaminates the wrapped tool's stdout (which
// may be piped). A --json mode emits one event object per line instead.
package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/FusionCube18712/claude-auto-resume/internal/notify"
	"github.com/FusionCube18712/claude-auto-resume/internal/resettime"
)

// Reporter implements supervisor.Reporter.
type Reporter struct {
	w           io.Writer
	quiet       bool
	jsonMode    bool
	color       bool
	notify      bool
	interactive bool
	nl          string
	tickActive  bool
	started     bool
}

// Options configures a Reporter.
type Options struct {
	Out    io.Writer
	Quiet  bool
	JSON   bool
	Color  bool
	Notify bool
	// Interactive enables the in-place live countdown. When false (stderr is a
	// pipe/file), per-second ticks are suppressed so logs aren't flooded.
	Interactive bool
	// CRLF terminates lines with \r\n, required when the controlling terminal
	// is in raw mode (output post-processing is off).
	CRLF bool
}

func New(o Options) *Reporter {
	nl := "\n"
	if o.CRLF {
		nl = "\r\n"
	}
	return &Reporter{
		w: o.Out, quiet: o.Quiet, jsonMode: o.JSON, color: o.Color,
		notify: o.Notify, interactive: o.Interactive, nl: nl,
	}
}

const (
	cReset  = "\x1b[0m"
	cYellow = "\x1b[33m"
	cGreen  = "\x1b[32m"
	cDim    = "\x1b[2m"
	clearLn = "\r\x1b[2K"
)

func (r *Reporter) paint(c, s string) string {
	if !r.color {
		return s
	}
	return c + s + cReset
}

// line prints a full line, first clearing any pending countdown.
func (r *Reporter) line(s string) {
	if r.tickActive {
		fmt.Fprint(r.w, clearLn)
		r.tickActive = false
	}
	fmt.Fprint(r.w, s+r.nl)
}

func (r *Reporter) emitJSON(event string, kv map[string]any) {
	kv["event"] = event
	kv["ts"] = time.Now().Format(time.RFC3339)
	b, _ := json.Marshal(kv)
	fmt.Fprint(r.w, string(b)+r.nl)
}

func (r *Reporter) OnStart(argv []string, mode string) {
	if r.jsonMode {
		r.emitJSON("start", map[string]any{"argv": argv, "mode": mode})
		return
	}
	if r.quiet || r.started {
		return // banner once; re-spawns are silent
	}
	r.started = true
	r.line(r.paint(cDim, fmt.Sprintf("claude-auto-resume: supervising %q (mode: %s)", argv[0], mode)))
}

func (r *Reporter) OnLimit(res resettime.Result, resumeAt time.Time, capped bool) {
	if r.notify {
		notify.Send("Usage limit reached", "Waiting until "+resumeAt.Local().Format("3:04 PM MST")+" to resume")
	}
	if r.jsonMode {
		r.emitJSON("limit", map[string]any{
			"reset": res.Reset.Format(time.RFC3339), "resume_at": resumeAt.Format(time.RFC3339),
			"source": string(res.Source), "matched": res.Matched, "capped": capped, "tz_note": res.TZNote,
		})
		return
	}
	at := resumeAt.Local().Format("Mon 3:04 PM MST")
	msg := fmt.Sprintf("⏸  Usage limit reached — resuming at %s", at)
	r.line(r.paint(cYellow, msg))
	if res.TZNote != "" {
		r.line(r.paint(cDim, "   note: "+res.TZNote))
	}
	if capped {
		r.line(r.paint(cDim, "   (reset was further out than --max-wait; capped)"))
	}
	if !res.Matched {
		r.line(r.paint(cDim, "   (couldn't parse a reset time; using fallback — will recheck if it limits again)"))
	}
}

func (r *Reporter) OnTick(remaining time.Duration, resumeAt time.Time) {
	if r.quiet || r.jsonMode || !r.interactive {
		return
	}
	at := resumeAt.Local().Format("3:04 PM")
	msg := fmt.Sprintf("⏳  Resuming in %s  ·  at %s  ·  Ctrl-C to cancel", fmtDuration(remaining), at)
	fmt.Fprint(r.w, clearLn+r.paint(cYellow, msg))
	r.tickActive = true
}

func (r *Reporter) OnResume(mode, detail string) {
	if r.notify {
		notify.Send("Resuming", "Usage limit reset — continuing")
	}
	if r.jsonMode {
		r.emitJSON("resume", map[string]any{"mode": mode, "detail": detail})
		return
	}
	r.line(r.paint(cGreen, fmt.Sprintf("▶  Resuming (%s)…", mode)))
}

func (r *Reporter) OnInfo(msg string) {
	if r.jsonMode {
		r.emitJSON("info", map[string]any{"msg": msg})
		return
	}
	if r.quiet {
		return
	}
	r.line(r.paint(cDim, "   "+msg))
}

// fmtDuration renders a duration as H:MM:SS (or MM:SS under an hour).
func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
