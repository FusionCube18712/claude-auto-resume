// Package profiles holds the per-tool knowledge: how each CLI announces a usage
// limit and how it should be resumed. Profiles are plain data so the config
// layer can extend them (extra --limit-regex, custom --resume) without code
// changes, which is how the tool stays useful for "any other CLI".
package profiles

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Profile describes one tool's limit signals and resume behaviour.
type Profile struct {
	Name string
	// LimitPatterns: any match in the (ANSI-stripped) output means "limited".
	LimitPatterns []*regexp.Regexp
	// WindowHours, if > 0, makes the reset-time fallback estimate the next
	// clock-aligned window boundary instead of a flat default wait.
	WindowHours int
	// PreferWrap is the default mode hint when auto-detection is ambiguous:
	// true => inject keystrokes into a live session, false => re-spawn headless.
	PreferWrap bool
	// InjectText is typed into the live session to resume (wrap mode).
	InjectText string
	// Respawn builds the argv to re-run for headless resume (exec mode), given
	// the original argv and the resume text.
	Respawn func(orig []string, resumeText string) []string
}

func reAll(pats ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(pats))
	for i, p := range pats {
		out[i] = regexp.MustCompile(p)
	}
	return out
}

// reExec re-runs the original command unchanged. Used for tools without a native
// resume (Codex, Gemini): the task restarts rather than continues — a documented
// limitation, but better than stopping dead.
func reExec(orig []string, _ string) []string { return orig }

var registry = map[string]Profile{
	"claude": {
		Name: "claude",
		LimitPatterns: reAll(
			`(?i)5-hour limit reached`,
			`(?i)weekly limit reached`,
			`(?i)you'?ve hit your limit`,
			`(?i)you'?re out of extra usage`,
			`(?i)out of extra usage`,
			`(?i)usage limit reached`,
			`(?i)rate_limit_error`,
			`(?i)\blimit reached\b.*\bresets?\b`,
		),
		WindowHours: 5,
		PreferWrap:  true,
		InjectText:  "continue",
		Respawn: func(orig []string, resume string) []string {
			bin := "claude"
			if len(orig) > 0 {
				bin = orig[0]
			}
			if resume == "" {
				resume = "continue"
			}
			return []string{bin, "--continue", resume}
		},
	},
	"codex": {
		Name: "codex",
		LimitPatterns: reAll(
			`(?i)rate limit reached`,
			`(?i)rate_limit_exceeded`,
			`(?i)you'?ve hit your.*limit`,
			`(?i)usage limit`,
			`(?i)try again in\s+[\d.]+\s*s`,
		),
		WindowHours: 0,
		PreferWrap:  false,
		InjectText:  "continue",
		Respawn:     reExec, // Codex has no native resume; re-run the command.
	},
	"gemini": {
		Name: "gemini",
		LimitPatterns: reAll(
			`(?i)resource_exhausted`,
			`(?i)rate limit`,
			`(?i)quota exceeded`,
			`(?i)status:?\s*429`,
		),
		WindowHours: 0,
		PreferWrap:  false,
		InjectText:  "continue",
		Respawn:     reExec,
	},
	"generic": {
		Name:          "generic",
		LimitPatterns: nil, // supplied via --limit-regex
		WindowHours:   0,
		PreferWrap:    false,
		InjectText:    "continue",
		Respawn:       reExec,
	},
}

// Get returns a copy of the named profile (so callers can mutate it freely) and
// whether it exists.
func Get(name string) (Profile, bool) {
	p, ok := registry[strings.ToLower(name)]
	if !ok {
		return Profile{}, false
	}
	// Copy the slice so appends by the caller don't corrupt the registry.
	cp := p
	cp.LimitPatterns = append([]*regexp.Regexp(nil), p.LimitPatterns...)
	return cp, true
}

// Names lists the built-in profile names (for help text).
func Names() []string { return []string{"claude", "codex", "gemini", "generic"} }

// Infer guesses a profile name from the wrapped command's program name.
func Infer(command string) string {
	base := strings.ToLower(filepath.Base(command))
	switch {
	case strings.Contains(base, "claude"):
		return "claude"
	case strings.Contains(base, "codex"):
		return "codex"
	case strings.Contains(base, "gemini"):
		return "gemini"
	default:
		return "generic"
	}
}
