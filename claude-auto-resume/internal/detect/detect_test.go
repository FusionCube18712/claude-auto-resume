package detect

import (
	"regexp"
	"testing"
)

func TestStripANSI(t *testing.T) {
	in := "\x1b[1;31mWeekly limit reached\x1b[0m \x1b[2m∙ resets 6pm\x1b[0m"
	want := "Weekly limit reached ∙ resets 6pm"
	if got := string(StripANSI([]byte(in))); got != want {
		t.Fatalf("StripANSI = %q, want %q", got, want)
	}
}

func TestDetectorMatchesAcrossChunks(t *testing.T) {
	d := New([]*regexp.Regexp{regexp.MustCompile(`(?i)limit reached`)}, 1024)

	if hit, _ := d.Feed([]byte("...working... \x1b[31mlimit re")); hit {
		t.Fatal("should not match on partial message")
	}
	hit, ctx := d.Feed([]byte("ached ∙ resets 2am\x1b[0m\n"))
	if !hit {
		t.Fatal("expected match once full message arrived across chunks")
	}
	if want := "limit reached ∙ resets 2am"; !regexp.MustCompile(want).MatchString(ctx) {
		t.Fatalf("context %q missing %q", ctx, want)
	}
}

func TestDetectorLatchesUntilReset(t *testing.T) {
	d := New([]*regexp.Regexp{regexp.MustCompile(`limit reached`)}, 1024)
	if hit, _ := d.Feed([]byte("limit reached now")); !hit {
		t.Fatal("expected first hit")
	}
	if hit, _ := d.Feed([]byte(" still limit reached")); hit {
		t.Fatal("should stay latched until Reset")
	}
	d.Reset()
	if hit, _ := d.Feed([]byte("limit reached again")); !hit {
		t.Fatal("expected hit after Reset")
	}
}

func TestAltScreenTracking(t *testing.T) {
	d := New(nil, 1024)
	if d.AltScreen() {
		t.Fatal("should start on main screen")
	}
	d.Feed([]byte("\x1b[?1049h")) // enter alt screen
	if !d.AltScreen() {
		t.Fatal("should detect alt-screen enter")
	}
	d.Feed([]byte("\x1b[?1049l")) // leave
	if d.AltScreen() {
		t.Fatal("should detect alt-screen leave")
	}
}
