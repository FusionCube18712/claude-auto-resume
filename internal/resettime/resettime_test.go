package resettime

import (
	"strconv"
	"testing"
	"time"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestParseClock(t *testing.T) {
	utc := time.UTC
	now := time.Date(2026, 6, 16, 9, 0, 0, 0, utc) // 09:00 UTC

	tests := []struct {
		name string
		text string
		opts Options
		want time.Time
	}{
		{
			name: "pm same day with explicit UTC",
			text: "You've hit your limit · resets 2pm (UTC)",
			opts: Options{Local: utc},
			want: time.Date(2026, 6, 16, 14, 0, 0, 0, utc),
		},
		{
			name: "no tz uses local, rolls to tomorrow when passed",
			text: "5-hour limit reached ∙ resets 2am",
			opts: Options{Local: utc},
			// 02:00 already passed at 09:00 -> next day 02:00 UTC
			want: time.Date(2026, 6, 17, 2, 0, 0, 0, utc),
		},
		{
			name: "half past with IANA zone (legacy alias)",
			text: "Limit reached · resets 11:30am (Asia/Calcutta) · /upgrade to Max",
			opts: Options{Local: utc},
			// 11:30 IST (UTC+5:30) == 06:00 UTC, after 09:00? no -> wait,
			// 06:00 UTC < 09:00 UTC so it should roll to next day.
			want: time.Date(2026, 6, 17, 6, 0, 0, 0, utc),
		},
		{
			name: "24h clock no ampm",
			text: "resets 14:00",
			opts: Options{Local: utc},
			want: time.Date(2026, 6, 16, 14, 0, 0, 0, utc),
		},
		{
			name: "12am means midnight",
			text: "resets 12am (UTC)",
			opts: Options{Local: utc},
			want: time.Date(2026, 6, 17, 0, 0, 0, 0, utc), // midnight already passed -> tomorrow
		},
		{
			name: "12pm means noon",
			text: "resets 12pm (UTC)",
			opts: Options{Local: utc},
			want: time.Date(2026, 6, 16, 12, 0, 0, 0, utc),
		},
		{
			name: "weekly limit, evening",
			text: "Weekly limit reached ∙ resets 6pm (UTC)",
			opts: Options{Local: utc},
			want: time.Date(2026, 6, 16, 18, 0, 0, 0, utc),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.text, now, tc.opts)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if got.Source != SourceClock {
				t.Fatalf("source = %s, want clock", got.Source)
			}
			if !got.Reset.Equal(tc.want) {
				t.Fatalf("reset = %s, want %s", got.Reset.UTC(), tc.want.UTC())
			}
		})
	}
}

// TestParseClockDST proves we delegate to zoneinfo rather than doing manual
// offset math: a reset given in a US zone the morning after spring-forward must
// land on EDT (UTC-4), not EST (UTC-5).
func TestParseClockDST(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	// 2026-03-08 is the US spring-forward date (02:00 EST -> 03:00 EDT).
	now := time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC) // 05:00 EDT
	got, err := Parse("resets 8am (America/New_York)", now, Options{Local: ny})
	if err != nil {
		t.Fatal(err)
	}
	// 8:00 EDT == 12:00 UTC.
	want := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	if !got.Reset.Equal(want) {
		t.Fatalf("DST reset = %s, want %s", got.Reset.UTC(), want)
	}
}

func TestParseRelative(t *testing.T) {
	now := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		text string
		want time.Duration
	}{
		{"Rate limit reached for o3. Please try again in 45.622s. Visit url", 45622 * time.Millisecond},
		{"retry after 2m30s", 150 * time.Second},
		{"try again in 90s", 90 * time.Second},
		{"try again in 1m", time.Minute},
		{"please try again in 45", 45 * time.Second},
		{"try again in 2 minutes", 2 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			got, err := Parse(tc.text, now, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Source != SourceRelative {
				t.Fatalf("source = %s, want relative", got.Source)
			}
			if !got.Reset.Equal(now.Add(tc.want)) {
				t.Fatalf("reset = %s, want now+%s (%s)", got.Reset, tc.want, now.Add(tc.want))
			}
		})
	}
}

func TestParseEpoch(t *testing.T) {
	now := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)

	t.Run("seconds", func(t *testing.T) {
		ts := now.Add(2 * time.Hour).Unix()
		text := "Claude AI usage limit reached|" + itoa(ts)
		got, err := Parse(text, now, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != SourceEpoch {
			t.Fatalf("source = %s, want epoch", got.Source)
		}
		if got.Reset.Unix() != ts {
			t.Fatalf("reset unix = %d, want %d", got.Reset.Unix(), ts)
		}
	})

	t.Run("millis", func(t *testing.T) {
		ms := now.Add(3 * time.Hour).UnixMilli()
		text := "usage limit reached | " + itoa(ms)
		got, err := Parse(text, now, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != SourceEpoch || got.Reset.UnixMilli() != ms {
			t.Fatalf("got %s @ %d, want epoch @ %d", got.Source, got.Reset.UnixMilli(), ms)
		}
	})

	t.Run("past epoch is ignored", func(t *testing.T) {
		ts := now.Add(-time.Hour).Unix()
		text := "limit reached|" + itoa(ts)
		got, _ := Parse(text, now, Options{DefaultWait: time.Hour})
		if got.Source == SourceEpoch {
			t.Fatalf("past epoch should not match, got %s", got.Source)
		}
	})
}

func TestFallback(t *testing.T) {
	utc := time.UTC
	now := time.Date(2026, 6, 16, 9, 0, 0, 0, utc)

	t.Run("default wait", func(t *testing.T) {
		got, err := Parse("nothing useful here", now, Options{DefaultWait: 90 * time.Minute, Local: utc})
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != SourceDefault || got.Matched {
			t.Fatalf("got %s matched=%v, want default unmatched", got.Source, got.Matched)
		}
		if !got.Reset.Equal(now.Add(90 * time.Minute)) {
			t.Fatalf("reset = %s, want now+90m", got.Reset)
		}
	})

	t.Run("window boundary", func(t *testing.T) {
		got, err := Parse("nothing useful here", now, Options{WindowHours: 5, Local: utc})
		if err != nil {
			t.Fatal(err)
		}
		// boundaries at 00,05,10,15,20 -> next after 09:00 is 10:00.
		want := time.Date(2026, 6, 16, 10, 0, 0, 0, utc)
		if got.Source != SourceWindow || !got.Reset.Equal(want) {
			t.Fatalf("got %s @ %s, want window @ %s", got.Source, got.Reset.UTC(), want)
		}
	})

	t.Run("empty errors but still returns usable result", func(t *testing.T) {
		got, err := Parse("   ", now, Options{DefaultWait: time.Hour, Local: utc})
		if err == nil {
			t.Fatal("expected error for empty input")
		}
		if got.Reset.Before(now) {
			t.Fatal("fallback reset should be in the future")
		}
	})
}

func TestUnresolvableZoneFlagged(t *testing.T) {
	utc := time.UTC
	now := time.Date(2026, 6, 16, 9, 0, 0, 0, utc)
	got, err := Parse("resets 9am (Mars/Phobos)", now, Options{Local: utc})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceClock {
		t.Fatalf("source = %s, want clock", got.Source)
	}
	if got.TZNote == "" {
		t.Fatal("expected a TZNote for an unresolvable zone")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
