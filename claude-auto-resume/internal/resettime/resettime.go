// Package resettime turns a usage-limit message into the absolute instant at
// which the limit resets. It is deliberately pure and clock-injectable: every
// entry point takes an explicit `now`, so the whole thing is unit-testable
// without sleeping or depending on the wall clock.
//
// Strategies are tried in priority order (most explicit first):
//
//  1. epoch     — "...usage limit reached|1739491200" (unix seconds or millis)
//  2. relative  — "Please try again in 45.6s" / "retry after 2m30s"
//  3. clock     — "resets 2pm (UTC)" / "resets 11:30am (Asia/Calcutta)"
//  4. fallback  — nothing parsed: next window boundary, else a fixed default
//
// Imperfect parses are safe by design: the supervisor re-detects the limit if
// it resumes too early, so an under-estimate just means one more short wait.
package resettime

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Source identifies which strategy produced a Result. Surfaced in logs and in
// the `--check-parse` diagnostic so format drift is visible, not silent.
type Source string

const (
	SourceEpoch    Source = "epoch"
	SourceRelative Source = "relative"
	SourceClock    Source = "clock"
	SourceWindow   Source = "window-fallback"
	SourceDefault  Source = "default-fallback"
)

// Result is the outcome of a parse.
type Result struct {
	Reset     time.Time // absolute instant the limit resets (no safety buffer applied)
	Source    Source    // which strategy matched
	Raw       string    // the substring that matched, for logging
	Matched   bool      // true if a real signal was found (false = pure fallback)
	TZNote    string    // non-empty when a named zone could not be resolved
	WallClock string    // human form of the parsed reset, when meaningful
}

// Options tunes fallback behaviour and timezone assumptions.
type Options struct {
	// DefaultWait is used when nothing parses and WindowHours <= 0.
	DefaultWait time.Duration
	// WindowHours, if > 0, makes the fallback estimate the next clock-aligned
	// window boundary (e.g. 5 => 00:00,05:00,10:00,15:00,20:00 local). Claude's
	// rolling window is not clock-aligned, so this is only a heuristic backstop.
	WindowHours int
	// Local is the location assumed when a message gives a clock time with no
	// timezone. Defaults to time.Local.
	Local *time.Location
}

func (o Options) local() *time.Location {
	if o.Local != nil {
		return o.Local
	}
	return time.Local
}

func (o Options) defaultWait() time.Duration {
	if o.DefaultWait > 0 {
		return o.DefaultWait
	}
	return time.Hour
}

var (
	// epoch: "...reached|1739491200" or "...reached | 1739491200000"
	reEpoch = regexp.MustCompile(`(?i)limit\s+reached\s*\|\s*(\d{10,13})`)

	// relative: capture the phrase that introduces a wait, then scan the tail
	// for duration components separately (handles "1m30s", "45.622s", "90s",
	// "2 minutes", bare "in 45").
	reRelTrigger = regexp.MustCompile(`(?i)(?:try again in|retry[\s-]?after|retry in|again in|wait)\s+([0-9][0-9hms.,:\s]*[0-9hmsa-z]|\d+)`)
	reRelPart    = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(ms|h|hours?|hrs?|m|min|mins|minutes?|s|sec|secs|seconds?)?`)

	// clock: "resets[ at] 2pm (UTC)" / "resetting 11:30 am (Asia/Calcutta)" /
	// "resets 14:00". Time is required; am/pm and the (zone) are optional.
	reClock = regexp.MustCompile(`(?i)reset(?:s|ting)?(?:\s+at)?\s+(\d{1,2})(?::(\d{2}))?\s*([ap]\.?m\.?)?(?:\s*\(([^)]+)\))?`)
)

// Parse resolves the reset instant for the limit message in text, relative to
// now. It always returns a usable Result (falling back when nothing parses);
// the returned error is non-nil only if the input is empty.
func Parse(text string, now time.Time, opts Options) (Result, error) {
	if strings.TrimSpace(text) == "" {
		return fallback(now, opts), errors.New("empty input")
	}
	for _, fn := range []func(string, time.Time, Options) (Result, bool){
		parseEpoch, parseRelative, parseClock,
	} {
		if r, ok := fn(text, now, opts); ok {
			return r, nil
		}
	}
	return fallback(now, opts), nil
}

func parseEpoch(text string, now time.Time, _ Options) (Result, bool) {
	m := reEpoch.FindStringSubmatch(text)
	if m == nil {
		return Result{}, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return Result{}, false
	}
	var t time.Time
	if len(m[1]) >= 13 {
		t = time.UnixMilli(n)
	} else {
		t = time.Unix(n, 0)
	}
	// Sanity: a reset epoch should be in the future and not absurdly far out.
	if !t.After(now) || t.Sub(now) > 30*24*time.Hour {
		return Result{}, false
	}
	return Result{
		Reset: t, Source: SourceEpoch, Raw: m[0], Matched: true,
		WallClock: t.Local().Format("Mon 3:04 PM MST"),
	}, true
}

func parseRelative(text string, now time.Time, _ Options) (Result, bool) {
	m := reRelTrigger.FindStringSubmatch(text)
	if m == nil {
		return Result{}, false
	}
	d, ok := parseDurationLoose(m[1])
	if !ok || d <= 0 {
		return Result{}, false
	}
	t := now.Add(d)
	return Result{
		Reset: t, Source: SourceRelative, Raw: strings.TrimSpace(m[0]), Matched: true,
		WallClock: t.Local().Format("Mon 3:04 PM MST"),
	}, true
}

// parseDurationLoose sums duration components found in s. A bare integer with no
// unit is treated as seconds (matching how rate-limit messages read).
func parseDurationLoose(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Bare number, no letters at all -> seconds.
	if !strings.ContainsAny(s, "hmsHMS") {
		if f, err := strconv.ParseFloat(strings.TrimRight(s, ".,"), 64); err == nil {
			return time.Duration(f * float64(time.Second)), true
		}
	}
	var total time.Duration
	found := false
	for _, part := range reRelPart.FindAllStringSubmatch(s, -1) {
		if part[1] == "" {
			continue
		}
		f, err := strconv.ParseFloat(part[1], 64)
		if err != nil {
			continue
		}
		unit := strings.ToLower(part[2])
		var d time.Duration
		switch {
		case unit == "ms":
			d = time.Duration(f * float64(time.Millisecond))
		case unit == "" || strings.HasPrefix(unit, "s"):
			d = time.Duration(f * float64(time.Second))
		case unit == "m" || strings.HasPrefix(unit, "min"):
			d = time.Duration(f * float64(time.Minute))
		case strings.HasPrefix(unit, "h"):
			d = time.Duration(f * float64(time.Hour))
		default:
			d = time.Duration(f * float64(time.Second))
		}
		if d > 0 {
			total += d
			found = true
		}
	}
	return total, found
}

func parseClock(text string, now time.Time, opts Options) (Result, bool) {
	m := reClock.FindStringSubmatch(text)
	if m == nil {
		return Result{}, false
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil || hour > 23 {
		return Result{}, false
	}
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	if minute > 59 {
		return Result{}, false
	}

	ampm := strings.ToLower(strings.ReplaceAll(m[3], ".", ""))
	switch ampm {
	case "am":
		if hour == 12 {
			hour = 0
		}
	case "pm":
		if hour != 12 {
			hour += 12
		}
	default:
		// 24h clock as written; nothing to adjust.
	}
	if hour > 23 {
		return Result{}, false
	}

	loc, tzNote := resolveZone(m[4], opts.local())

	// Next occurrence of hour:minute in loc that is strictly after now.
	nowLoc := now.In(loc)
	cand := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), hour, minute, 0, 0, loc)
	if !cand.After(now) {
		cand = cand.AddDate(0, 0, 1)
	}
	return Result{
		Reset: cand, Source: SourceClock, Raw: strings.TrimSpace(m[0]), Matched: true,
		TZNote: tzNote, WallClock: cand.Format("Mon 3:04 PM MST"),
	}, true
}

// commonAbbrev maps timezone abbreviations that time.LoadLocation cannot resolve
// to IANA zones. Only unambiguous ones are included; ambiguous abbreviations
// (e.g. IST, CST) are intentionally omitted to avoid a confidently-wrong guess.
var commonAbbrev = map[string]string{
	"UTC": "UTC", "GMT": "UTC", "Z": "UTC",
	"PT": "America/Los_Angeles", "PST": "America/Los_Angeles", "PDT": "America/Los_Angeles",
	"MT": "America/Denver", "MST": "America/Denver", "MDT": "America/Denver",
	"ET": "America/New_York", "EST": "America/New_York", "EDT": "America/New_York",
	"BST": "Europe/London", "CET": "Europe/Paris", "CEST": "Europe/Paris",
}

// resolveZone resolves a timezone token from a limit message. It returns the
// fallback location and a note when the token names a zone we cannot resolve,
// so the caller can surface low confidence instead of silently being hours off.
func resolveZone(tok string, fallback *time.Location) (*time.Location, string) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return fallback, ""
	}
	if loc, err := time.LoadLocation(tok); err == nil {
		return loc, ""
	}
	if iana, ok := commonAbbrev[strings.ToUpper(tok)]; ok {
		if loc, err := time.LoadLocation(iana); err == nil {
			return loc, ""
		}
	}
	return fallback, "could not resolve timezone " + strconv.Quote(tok) + "; assuming " + fallback.String()
}

func fallback(now time.Time, opts Options) Result {
	if opts.WindowHours > 0 {
		t := nextWindowBoundary(now, opts.WindowHours, opts.local())
		return Result{Reset: t, Source: SourceWindow, Matched: false, WallClock: t.Format("Mon 3:04 PM MST")}
	}
	t := now.Add(opts.defaultWait())
	return Result{Reset: t, Source: SourceDefault, Matched: false, WallClock: t.Local().Format("Mon 3:04 PM MST")}
}

// nextWindowBoundary returns the next clock-aligned boundary after now, where
// boundaries fall every windowHours hours from local midnight.
func nextWindowBoundary(now time.Time, windowHours int, loc *time.Location) time.Time {
	if windowHours <= 0 {
		windowHours = 5
	}
	nl := now.In(loc)
	midnight := time.Date(nl.Year(), nl.Month(), nl.Day(), 0, 0, 0, 0, loc)
	for h := 0; h <= 24; h += windowHours {
		cand := midnight.Add(time.Duration(h) * time.Hour)
		if cand.After(now) {
			return cand
		}
	}
	// Past the last boundary today; first boundary tomorrow.
	return midnight.AddDate(0, 0, 1)
}
