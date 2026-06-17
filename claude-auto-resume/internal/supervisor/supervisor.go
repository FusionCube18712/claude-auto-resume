// Package supervisor runs a wrapped CLI, watches its output for a usage limit,
// waits until the limit resets, and resumes it — either by typing into the live
// session (wrap mode, for TUIs) or by re-spawning a headless command (exec
// mode). The loop is built on the `stream` and `Clock` abstractions so it can be
// exercised over plain pipes with a fake clock, no real sleeping or PTY.
package supervisor

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FusionCube18712/claude-auto-resume/internal/profiles"
	"github.com/FusionCube18712/claude-auto-resume/internal/resettime"
)

// Reporter receives lifecycle events for display. All methods may be called
// from the supervisor goroutine; implementations should be cheap.
type Reporter interface {
	OnStart(argv []string, mode string)
	OnLimit(res resettime.Result, resumeAt time.Time, capped bool)
	OnTick(remaining time.Duration, resumeAt time.Time)
	OnResume(mode, detail string)
	OnInfo(msg string)
}

// Config fully describes one supervised session.
type Config struct {
	Argv    []string
	Profile profiles.Profile
	Mode    string // "auto" | "wrap" | "exec"

	ResumeText  string
	Buffer      time.Duration
	MaxWait     time.Duration
	DefaultWait time.Duration
	MaxRetries  int

	UsePTY bool
	Env    []string
	Dir    string

	// Stdin is the terminal input forwarded to the child. nil disables the
	// stdin pump (used by tests). Stdout receives the child's output.
	Stdin  readCloser
	Stdout writer

	// Interrupt is invoked when the user presses Ctrl-C during a wait (raw mode
	// swallows SIGINT, so the pump detects it from the byte stream). Typically
	// wired to the root context's cancel.
	Interrupt func()

	Clock    Clock
	Reporter Reporter

	// newStream, if set, overrides child creation (tests inject scripted streams).
	newStream func(argv []string) (stream, error)
}

type readCloser interface {
	Read([]byte) (int, error)
}
type writer interface {
	Write([]byte) (int, error)
}

// Run supervises the command to completion, returning the final child exit code.
func Run(ctx context.Context, cfg Config) (int, error) {
	s := newSession(cfg)
	return s.run(ctx)
}

type session struct {
	cfg      Config
	clock    Clock
	reporter Reporter
	detector limitFeeder

	mu        sync.Mutex
	limited   bool
	lastLimit string

	waiting    atomic.Bool
	lastResume time.Time
	fastCount  int

	fastThreshold time.Duration
	stopResize    func()
}

// limitFeeder is the subset of detect.Detector the supervisor needs (kept as an
// interface so tests can substitute a trivial matcher).
type limitFeeder interface {
	Feed(raw []byte) (bool, string)
	AltScreen() bool
	Reset()
}

func newSession(cfg Config) *session {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.Reporter == nil {
		cfg.Reporter = nopReporter{}
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 5
	}
	if cfg.Buffer < 0 {
		cfg.Buffer = 0
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 8 * time.Hour
	}
	return &session{
		cfg:           cfg,
		clock:         cfg.Clock,
		reporter:      cfg.Reporter,
		detector:      newDetector(cfg.Profile),
		fastThreshold: 90 * time.Second,
	}
}

const (
	outFinished = iota
	outCanceled
	outRespawn
	outError
)

type outcome struct {
	kind int
	code int
	err  error
	fast bool
}

func (s *session) run(ctx context.Context) (int, error) {
	argv := s.cfg.Argv
	for {
		st, err := s.spawn(argv)
		if err != nil {
			return 1, fmt.Errorf("starting %q: %w", argv[0], err)
		}
		out := s.monitor(ctx, st)
		if s.stopResize != nil {
			s.stopResize()
			s.stopResize = nil
		}
		switch out.kind {
		case outFinished:
			return out.code, nil
		case outCanceled:
			st.Kill()
			return 130, nil
		case outError:
			st.Kill()
			return 1, out.err
		case outRespawn:
			if out.fast {
				s.fastCount++
			} else {
				s.fastCount = 0
			}
			if s.fastCount > s.cfg.MaxRetries {
				return 1, fmt.Errorf("aborting: hit the limit %d times in a row without making progress", s.fastCount)
			}
			argv = s.cfg.Profile.Respawn(s.cfg.Argv, s.resumeText())
			s.reporter.OnResume("exec", argv[0])
			s.detector.Reset()
			s.mu.Lock()
			s.limited = false
			s.mu.Unlock()
			s.markResumed()
		}
	}
}

func (s *session) monitor(ctx context.Context, st stream) outcome {
	spawnAt := s.clock.Now()
	limitCh := make(chan struct{}, 1)
	readDone := make(chan struct{})

	// Reader: child output -> our stdout + the limit detector. Drains fully
	// before signalling readDone, so a limit printed just before exit is never
	// lost to the exit race.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := st.Read(buf)
			if n > 0 {
				s.cfg.Stdout.Write(buf[:n])
				if hit, c := s.detector.Feed(buf[:n]); hit {
					s.mu.Lock()
					s.limited = true
					s.lastLimit = c
					s.mu.Unlock()
					select {
					case limitCh <- struct{}{}:
					default:
					}
				}
			}
			if err != nil {
				break
			}
		}
		close(readDone)
	}()

	if s.cfg.Stdin != nil {
		stop := make(chan struct{})
		defer close(stop)
		go s.pumpStdin(st, stop)
	}

	for {
		select {
		case <-ctx.Done():
			return outcome{kind: outCanceled}

		case <-limitCh:
			if s.decideWrap() {
				if s.tooManyFast() {
					return outcome{kind: outError, err: s.tooManyErr()}
				}
				if err := s.waitForReset(ctx); err != nil {
					return outcome{kind: outCanceled}
				}
				s.inject(st)
				s.detector.Reset()
				s.mu.Lock()
				s.limited = false
				s.mu.Unlock()
				continue
			}
			// Headless: wait for the reset, make sure the child is gone, respawn.
			fast := s.clock.Now().Sub(spawnAt) < s.fastThreshold
			if err := s.waitForReset(ctx); err != nil {
				return outcome{kind: outCanceled}
			}
			st.Kill()
			<-readDone
			return outcome{kind: outRespawn, fast: fast}

		case <-readDone:
			s.mu.Lock()
			limited := s.limited
			s.mu.Unlock()
			if limited {
				fast := s.clock.Now().Sub(spawnAt) < s.fastThreshold
				if err := s.waitForReset(ctx); err != nil {
					return outcome{kind: outCanceled}
				}
				return outcome{kind: outRespawn, fast: fast}
			}
			_ = st.Wait()
			return outcome{kind: outFinished, code: st.ExitCode()}
		}
	}
}

// decideWrap chooses between typing into the live session (wrap) and
// re-spawning a headless command (exec).
func (s *session) decideWrap() bool {
	switch s.cfg.Mode {
	case "wrap":
		return true
	case "exec":
		return false
	default: // auto: only the live-TUI case can be resumed by injection.
		return s.cfg.UsePTY && s.detector.AltScreen()
	}
}

func (s *session) waitForReset(ctx context.Context) error {
	s.mu.Lock()
	text := s.lastLimit
	s.mu.Unlock()

	res, _ := resettime.Parse(text, s.clock.Now(), s.resetOpts())
	resumeAt := res.Reset.Add(s.cfg.Buffer)

	capped := false
	if max := s.clock.Now().Add(s.cfg.MaxWait); resumeAt.After(max) {
		resumeAt = max
		capped = true
	}
	s.reporter.OnLimit(res, resumeAt, capped)

	s.waiting.Store(true)
	defer s.waiting.Store(false)
	for {
		rem := resumeAt.Sub(s.clock.Now())
		if rem <= 0 {
			return nil
		}
		s.reporter.OnTick(rem, resumeAt)
		step := time.Second
		if rem < step {
			step = rem
		}
		if err := s.clock.Sleep(ctx, step); err != nil {
			return err
		}
	}
}

func (s *session) resetOpts() resettime.Options {
	return resettime.Options{
		DefaultWait: s.cfg.DefaultWait,
		WindowHours: s.cfg.Profile.WindowHours,
		Local:       time.Local,
	}
}

func (s *session) inject(st stream) {
	text := s.resumeText()
	st.Write([]byte(text + "\r"))
	s.reporter.OnResume("wrap", text)
	s.markResumed()
}

func (s *session) resumeText() string {
	if s.cfg.ResumeText != "" {
		return s.cfg.ResumeText
	}
	if s.cfg.Profile.InjectText != "" {
		return s.cfg.Profile.InjectText
	}
	return "continue"
}

func (s *session) markResumed() { s.lastResume = s.clock.Now() }

// tooManyFast records a wrap-mode re-limit and reports whether the loop is
// hammering (limited again within fastThreshold of the last resume).
func (s *session) tooManyFast() bool {
	now := s.clock.Now()
	if !s.lastResume.IsZero() && now.Sub(s.lastResume) < s.fastThreshold {
		s.fastCount++
	} else {
		s.fastCount = 0
	}
	return s.fastCount > s.cfg.MaxRetries
}

func (s *session) tooManyErr() error {
	return fmt.Errorf("aborting: hit the limit %d times in a row without making progress", s.fastCount)
}

// pumpStdin forwards terminal input to the child. While a wait is in progress it
// also watches for Ctrl-C (raw mode swallows SIGINT) and triggers Interrupt.
func (s *session) pumpStdin(st stream, stop <-chan struct{}) {
	buf := make([]byte, 1024)
	for {
		select {
		case <-stop:
			return
		default:
		}
		n, err := s.cfg.Stdin.Read(buf)
		if n > 0 {
			if s.waiting.Load() && containsETX(buf[:n]) {
				if s.cfg.Interrupt != nil {
					s.cfg.Interrupt()
				}
				return
			}
			if _, werr := st.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func containsETX(b []byte) bool {
	for _, c := range b {
		if c == 0x03 {
			return true
		}
	}
	return false
}

type nopReporter struct{}

func (nopReporter) OnStart([]string, string)                  {}
func (nopReporter) OnLimit(resettime.Result, time.Time, bool) {}
func (nopReporter) OnTick(time.Duration, time.Time)           {}
func (nopReporter) OnResume(string, string)                   {}
func (nopReporter) OnInfo(string)                             {}
