package main

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestSlogLevelToken(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{`time=2026-01-01T00:00:00Z level=INFO msg="poll complete" repos=1`, "INFO"},
		{`time=2026-01-01T00:00:00Z level=ERROR msg="boom"`, "ERROR"},
		{"plain text with no fields", ""},
		{`msg="a message that mentions level=INFO inside quotes"`, "INFO"}, // acceptable false positive on quoted content
		{"leveled=5 not-a-level-field", ""},
	}
	for _, c := range cases {
		if got := slogLevelToken(c.line); got != c.want {
			t.Errorf("slogLevelToken(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

func TestLevelLogFunc(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cases := []struct {
		token   string
		wantOK  bool
		wantLvl string
	}{
		{"DEBUG", true, "DEBUG"},
		{"info", true, "INFO"}, // case-insensitive
		{"WARN", true, "WARN"},
		{"WARNING", true, "WARN"},
		{"ERROR", true, "ERROR"},
		{"", false, ""},
		{"bogus", false, ""},
	}
	for _, c := range cases {
		fn, ok := levelLogFunc(log, c.token)
		if ok != c.wantOK {
			t.Errorf("levelLogFunc(%q) ok = %v, want %v", c.token, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		buf.Reset()
		fn("hello")
		if out := buf.String(); !strings.Contains(out, "level="+c.wantLvl) {
			t.Errorf("levelLogFunc(%q): got %q, want level=%s", c.token, out, c.wantLvl)
		}
	}
}

func TestStreamLines_preservesAgentsOwnLevel(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Simulates an agent's own slog-formatted stdout: a mix of levels, plus
	// one line with no level field at all (falls back to the caller's
	// default, Info here).
	input := strings.NewReader(strings.Join([]string{
		`time=2026-01-01T00:00:00Z level=INFO msg="poll complete" repos=1`,
		`time=2026-01-01T00:00:01Z level=ERROR msg="aide call failed" err="boom"`,
		`plain line with no level field`,
	}, "\n") + "\n")

	var wg sync.WaitGroup
	wg.Add(1)
	streamLines(&wg, input, log, log.Info)
	wg.Wait()

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 log lines, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "level=INFO") || !strings.Contains(lines[0], "poll complete") {
		t.Errorf("line 1 should preserve INFO: %s", lines[0])
	}
	if !strings.Contains(lines[1], "level=ERROR") || !strings.Contains(lines[1], "aide call failed") {
		t.Errorf("line 2 should preserve ERROR, not be flattened to the stdout default (Info): %s", lines[1])
	}
	if !strings.Contains(lines[2], "level=INFO") || !strings.Contains(lines[2], "plain line with no level field") {
		t.Errorf("line 3 (no level field) should use the caller's fallback (Info): %s", lines[2])
	}
}

func TestStreamLines_fallsBackForNonSlogOutput(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	input := strings.NewReader("a plain error message with no slog formatting\n")
	var wg sync.WaitGroup
	wg.Add(1)
	streamLines(&wg, input, log, log.Warn) // simulating the stderr default
	wg.Wait()

	if out := buf.String(); !strings.Contains(out, "level=WARN") {
		t.Errorf("expected the stderr default (Warn) for non-slog output, got: %s", out)
	}
}
