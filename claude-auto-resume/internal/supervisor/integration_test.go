package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/FusionCube18712/claude-auto-resume/internal/profiles"
)

var fakecliBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "car-it")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(dir, "fakecli")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", bin, "github.com/FusionCube18712/claude-auto-resume/fakecli").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "building fakecli: %v\n%s", err, out)
		os.Exit(1)
	}
	fakecliBin = bin
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func itProfile() profiles.Profile {
	return profiles.Profile{
		Name:          "generic",
		LimitPatterns: []*regexp.Regexp{regexp.MustCompile(`(?i)rate limit reached`)},
		InjectText:    "continue",
		Respawn:       func(orig []string, _ string) []string { return orig },
	}
}

// TestIntegrationExecRespawn runs the real fakecli binary: it limits and exits,
// the supervisor waits ~1s for the (fake) reset, re-spawns it, and the second
// run reports success.
func TestIntegrationExecRespawn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-process integration test in -short mode")
	}
	state := filepath.Join(t.TempDir(), "state")
	out := &syncBuffer{}
	cfg := Config{
		Argv:    []string{fakecliBin, state},
		Profile: itProfile(),
		Mode:    "exec",
		Buffer:  0,
		MaxWait: time.Minute,
		UsePTY:  false,
		Env:     os.Environ(),
		Stdout:  out,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run error: %v\noutput:\n%s", err, out.String())
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0\noutput:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "RESUMED-OK") {
		t.Fatalf("missing RESUMED-OK in output:\n%s", out.String())
	}
}

// TestIntegrationWrapInject runs the real fakecli as a fake TUI in a real PTY:
// it enters the alt screen, limits, and waits on stdin; the supervisor injects
// "continue", which the child reads and reports.
func TestIntegrationWrapInject(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-PTY integration test in -short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("PTY wrap test is exercised on Unix CI")
	}
	state := filepath.Join(t.TempDir(), "state")
	out := &syncBuffer{}
	cfg := Config{
		Argv:    []string{fakecliBin, "-mode", "wrap", state},
		Profile: itProfile(),
		Mode:    "wrap",
		Buffer:  0,
		MaxWait: time.Minute,
		UsePTY:  true,
		Env:     os.Environ(),
		Stdout:  out,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run error: %v\noutput:\n%s", err, out.String())
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0\noutput:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "RESUMED-OK") {
		t.Fatalf("missing RESUMED-OK in output:\n%s", out.String())
	}
}
