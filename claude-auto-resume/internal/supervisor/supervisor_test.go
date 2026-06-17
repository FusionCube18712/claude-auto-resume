package supervisor

import (
	"context"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FusionCube18712/claude-auto-resume/internal/profiles"
)

// fakeClock advances instantly on Sleep so wait loops terminate without delay.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return ctx.Err()
}

// fakeStream is a scripted, in-memory child process.
type fakeStream struct {
	readCh   chan []byte
	buf      []byte
	mu       sync.Mutex
	closed   bool
	code     int
	in       strings.Builder
	waitDone chan struct{}
	onWrite  func(f *fakeStream, p []byte)
}

func newFakeStream() *fakeStream {
	return &fakeStream{readCh: make(chan []byte, 32), waitDone: make(chan struct{})}
}

func (f *fakeStream) feed(s string) { f.readCh <- []byte(s) }

func (f *fakeStream) finish(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	f.code = code
	close(f.readCh)
	close(f.waitDone)
}

func (f *fakeStream) Read(p []byte) (int, error) {
	if len(f.buf) == 0 {
		b, ok := <-f.readCh
		if !ok {
			return 0, io.EOF
		}
		f.buf = b
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

func (f *fakeStream) Write(p []byte) (int, error) {
	f.in.WriteString(string(p))
	if f.onWrite != nil {
		f.onWrite(f, p)
	}
	return len(p), nil
}

func (f *fakeStream) Close() error          { return nil }
func (f *fakeStream) Resize(int, int) error { return nil }
func (f *fakeStream) Wait() error           { <-f.waitDone; return nil }
func (f *fakeStream) Kill() error           { f.finish(137); return nil }
func (f *fakeStream) ExitCode() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.code
}

// syncBuffer is a goroutine-safe sink for child output.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.WriteString(string(p))
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func genericProfile(pattern string) profiles.Profile {
	return profiles.Profile{
		Name:          "generic",
		LimitPatterns: []*regexp.Regexp{regexp.MustCompile(pattern)},
		InjectText:    "continue",
		Respawn:       func(orig []string, _ string) []string { return orig },
	}
}

func baseConfig(out *syncBuffer, clock *fakeClock, factory func(argv []string) (stream, error)) Config {
	return Config{
		Argv:      []string{"fake"},
		Profile:   genericProfile(`(?i)rate limit reached`),
		Mode:      "exec",
		Buffer:    0,
		MaxWait:   time.Hour,
		Stdout:    out,
		Clock:     clock,
		newStream: factory,
	}
}

func TestExecRespawnAfterLimit(t *testing.T) {
	out := &syncBuffer{}
	clock := &fakeClock{t: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)}

	var n int
	factory := func([]string) (stream, error) {
		st := newFakeStream()
		n++
		if n == 1 {
			go func() {
				st.feed("working...\nRate limit reached. Please try again in 1s.\n")
				st.finish(1)
			}()
		} else {
			go func() {
				st.feed("RESUMED and finished\n")
				st.finish(0)
			}()
		}
		return st, nil
	}

	code, err := Run(context.Background(), baseConfig(out, clock, factory))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "RESUMED") {
		t.Fatalf("output missing RESUMED:\n%s", out.String())
	}
	if n != 2 {
		t.Fatalf("expected 2 spawns, got %d", n)
	}
}

func TestWrapInjectResume(t *testing.T) {
	out := &syncBuffer{}
	clock := &fakeClock{t: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)}

	factory := func([]string) (stream, error) {
		st := newFakeStream()
		st.onWrite = func(f *fakeStream, p []byte) {
			if strings.Contains(string(p), "continue") {
				f.feed("RESUMED in place\n")
				f.finish(0)
			}
		}
		go st.feed("Rate limit reached. Please try again in 1s.\n")
		return st, nil
	}

	cfg := baseConfig(out, clock, factory)
	cfg.Mode = "wrap"
	code, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "RESUMED in place") {
		t.Fatalf("output missing resume marker:\n%s", out.String())
	}
}

func TestFinishesWithoutLimit(t *testing.T) {
	out := &syncBuffer{}
	clock := &fakeClock{t: time.Now()}
	factory := func([]string) (stream, error) {
		st := newFakeStream()
		go func() { st.feed("all good\n"); st.finish(0) }()
		return st, nil
	}
	code, err := Run(context.Background(), baseConfig(out, clock, factory))
	if err != nil || code != 0 {
		t.Fatalf("got (%d, %v), want (0, nil)", code, err)
	}
}

func TestAbortsAfterTooManyFastRelimits(t *testing.T) {
	out := &syncBuffer{}
	clock := &fakeClock{t: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)}
	factory := func([]string) (stream, error) {
		st := newFakeStream()
		go func() {
			st.feed("Rate limit reached. Please try again in 1s.\n")
			st.finish(1)
		}()
		return st, nil
	}
	cfg := baseConfig(out, clock, factory)
	cfg.MaxRetries = 3
	_, err := Run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "in a row") {
		t.Fatalf("expected too-many-relimits error, got %v", err)
	}
}

func TestContextCancelStops(t *testing.T) {
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	factory := func([]string) (stream, error) {
		st := newFakeStream()
		go st.feed("Rate limit reached. Please try again in 9h.\n") // long wait; never finishes
		return st, nil
	}
	cfg := baseConfig(out, nil, factory)
	cfg.Clock = realClock{} // real, blocking sleep so cancellation is observable
	cfg.MaxWait = 10 * time.Hour
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	code, _ := Run(ctx, cfg)
	if code != 130 {
		t.Fatalf("exit code = %d, want 130 on cancel", code)
	}
}
