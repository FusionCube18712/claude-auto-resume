// Command claude-auto-resume runs an AI coding CLI through a supervisor that
// waits out usage limits and resumes automatically. Despite the name it works
// for Claude Code, Codex, Gemini, and any other CLI via a generic profile.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/FusionCube18712/claude-auto-resume/internal/profiles"
	"github.com/FusionCube18712/claude-auto-resume/internal/resettime"
	"github.com/FusionCube18712/claude-auto-resume/internal/supervisor"
	"github.com/FusionCube18712/claude-auto-resume/internal/ui"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("claude-auto-resume", flag.ContinueOnError)
	fs.Usage = func() { usage(fs.Output()) }

	var (
		tool        = fs.String("tool", "", "profile: claude|codex|gemini|generic (default: inferred from the command)")
		mode        = fs.String("mode", "auto", "resume mode: auto|wrap|exec")
		resume      = fs.String("resume", "", "text to send on resume (default: profile's, usually \"continue\")")
		resumeCmd   = fs.String("resume-cmd", "", "headless resume command (space-separated) overriding the profile")
		buffer      = fs.Duration("buffer", 30*time.Second, "extra wait added after the parsed reset time")
		maxWait     = fs.Duration("max-wait", 8*time.Hour, "cap on how long to wait for a single reset")
		defaultWait = fs.Duration("default-wait", time.Hour, "wait used when no reset time can be parsed")
		maxRetries  = fs.Int("max-retries", 5, "abort after this many immediate re-limits without progress")
		noPTY       = fs.Bool("no-pty", false, "run with pipes instead of a PTY (CI / non-interactive)")
		notifyFlag  = fs.Bool("notify", false, "ring the bell and send a desktop notification on limit/resume")
		quiet       = fs.Bool("quiet", false, "suppress status output (errors still shown)")
		jsonMode    = fs.Bool("json", false, "emit machine-readable JSON events on stderr")
		checkParse  = fs.String("check-parse", "", "parse a limit string, print the resolved reset time, and exit")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	var limitRegexes stringList
	fs.Var(&limitRegexes, "limit-regex", "extra limit-detection regex (repeatable)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Println("claude-auto-resume", version)
		return 0
	}

	// Resolve the profile (needed by both --check-parse and a normal run).
	rest := fs.Args()
	toolName := *tool
	if toolName == "" && len(rest) > 0 {
		toolName = profiles.Infer(rest[0])
	}
	if toolName == "" {
		toolName = "claude"
	}
	prof, ok := profiles.Get(toolName)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown tool %q (known: %s)\n", toolName, strings.Join(profiles.Names(), ", "))
		return 2
	}
	for _, p := range limitRegexes {
		re, err := regexp.Compile(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --limit-regex %q: %v\n", p, err)
			return 2
		}
		prof.LimitPatterns = append(prof.LimitPatterns, re)
	}

	if *checkParse != "" {
		return doCheckParse(*checkParse, prof, *defaultWait, *buffer, *maxWait)
	}

	if len(rest) == 0 {
		usage(os.Stderr)
		return 2
	}
	switch *mode {
	case "auto", "wrap", "exec":
	default:
		fmt.Fprintf(os.Stderr, "invalid --mode %q (want auto|wrap|exec)\n", *mode)
		return 2
	}
	if *resumeCmd != "" {
		fields := strings.Fields(*resumeCmd)
		prof.Respawn = func([]string, string) []string { return fields }
	}
	if len(prof.LimitPatterns) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no limit patterns for this tool; add --limit-regex or nothing will be detected")
	}

	// Signals: SIGINT/SIGTERM cancel the run. Ctrl-C from a raw terminal is
	// delivered as a byte and handled by the supervisor's stdin pump, which also
	// calls cancel.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	usePTY := !*noPTY
	stdinFd := int(os.Stdin.Fd())
	rawMode := usePTY && term.IsTerminal(stdinFd)
	if rawMode {
		old, err := term.MakeRaw(stdinFd)
		if err == nil {
			defer term.Restore(stdinFd, old)
		} else {
			rawMode = false
		}
	}

	stderrTTY := term.IsTerminal(int(os.Stderr.Fd()))
	rep := ui.New(ui.Options{
		Out:         os.Stderr,
		Quiet:       *quiet,
		JSON:        *jsonMode,
		Color:       stderrTTY && !*jsonMode,
		Notify:      *notifyFlag,
		Interactive: stderrTTY && !*jsonMode,
		CRLF:        rawMode,
	})

	cfg := supervisor.Config{
		Argv:        rest,
		Profile:     prof,
		Mode:        *mode,
		ResumeText:  *resume,
		Buffer:      *buffer,
		MaxWait:     *maxWait,
		DefaultWait: *defaultWait,
		MaxRetries:  *maxRetries,
		UsePTY:      usePTY,
		Env:         os.Environ(),
		Stdout:      os.Stdout,
		Reporter:    rep,
		Interrupt:   cancel,
	}
	if rawMode {
		cfg.Stdin = os.Stdin
	}

	code, err := supervisor.Run(ctx, cfg)
	if err != nil {
		if rawMode {
			term.Restore(stdinFd, nil) // best-effort so the error prints cleanly
		}
		fmt.Fprintln(os.Stderr, "claude-auto-resume:", err)
	}
	return code
}

func doCheckParse(s string, prof profiles.Profile, defaultWait, buffer, maxWait time.Duration) int {
	now := time.Now()
	res, perr := resettime.Parse(s, now, resettime.Options{
		DefaultWait: defaultWait,
		WindowHours: prof.WindowHours,
		Local:       time.Local,
	})
	resumeAt := res.Reset.Add(buffer)
	capped := false
	if m := now.Add(maxWait); resumeAt.After(m) {
		resumeAt = m
		capped = true
	}

	matched := "yes"
	if !res.Matched {
		matched = "no (fell back)"
	}
	fmt.Printf("input:      %q\n", s)
	fmt.Printf("matched:    %s  (strategy: %s)\n", matched, res.Source)
	fmt.Printf("reset at:   %s  (%s)\n", res.Reset.Local().Format("Mon 2006-01-02 3:04:05 PM MST"), res.Reset.UTC().Format("15:04:05 UTC"))
	fmt.Printf("resume at:  %s  (+%s buffer)\n", resumeAt.Local().Format("Mon 2006-01-02 3:04:05 PM MST"), buffer)
	fmt.Printf("wait:       %s from now\n", resumeAt.Sub(now).Round(time.Second))
	if res.TZNote != "" {
		fmt.Printf("warning:    %s\n", res.TZNote)
	}
	if capped {
		fmt.Printf("warning:    capped to --max-wait (%s)\n", maxWait)
	}
	if perr != nil {
		fmt.Printf("note:       %v\n", perr)
	}
	return 0
}

func usage(w interface{ Write([]byte) (int, error) }) {
	fmt.Fprintf(w, `claude-auto-resume %s — wait out usage limits and resume your AI CLI

USAGE:
  claude-auto-resume [flags] [--] <command> [args...]

EXAMPLES:
  claude-auto-resume claude
  claude-auto-resume --tool codex -- codex exec "refactor the auth module"
  claude-auto-resume --tool generic --limit-regex 'quota exhausted' -- my-cli run
  claude-auto-resume --check-parse "5-hour limit reached ∙ resets 2am"

FLAGS:
  -tool          profile: claude|codex|gemini|generic (default: inferred)
  -mode          auto|wrap|exec (default auto: inject into a live TUI, else re-spawn)
  -resume        text to send on resume (default "continue")
  -resume-cmd    headless resume command overriding the profile
  -limit-regex   extra limit-detection regex (repeatable)
  -buffer        extra wait after the reset time (default 30s)
  -max-wait      cap on a single wait (default 8h)
  -default-wait  wait when no reset time parses (default 1h)
  -max-retries   abort after N immediate re-limits (default 5)
  -no-pty        use pipes instead of a PTY (CI)
  -notify        bell + desktop notification on limit/resume
  -quiet         suppress status output
  -json          machine-readable JSON events on stderr
  -check-parse   parse a limit string, print the reset time, and exit
  -version       print version and exit

Flags must come before the wrapped command. Everything after the command
(or a literal --) is passed through unchanged.
`, version)
}
