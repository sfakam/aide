package pty

import "testing"

// ─── StripANSI ───────────────────────────────────────────────────────────────

func TestStripANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[31mhello\x1b[0m", "hello"},
		{"\x1b[38;5;153mNo,\x1b[12Gexit", "No,exit"},
		{"plain text", "plain text"},
		{"\x1b]0;title\x07text", "text"},
		// Claude's prompt character should survive (not an ANSI code)
		{"❯ type here", "❯ type here"},
	}
	for _, c := range cases {
		got := StripANSI(c.in)
		if got != c.want {
			t.Errorf("StripANSI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ─── ExtractMarked ───────────────────────────────────────────────────────────

func TestExtractMarked_found(t *testing.T) {
	raw := "some preamble\n---RESPONSE---\nHello, world!\n---END---\ntrailing"
	body, ok := ExtractMarked(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if body != "Hello, world!" {
		t.Errorf("got %q", body)
	}
}

func TestExtractMarked_multiline(t *testing.T) {
	raw := "---RESPONSE---\nLine one\nLine two\n---END---"
	body, ok := ExtractMarked(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if body != "Line one\nLine two" {
		t.Errorf("got %q", body)
	}
}

func TestExtractMarked_no_start(t *testing.T) {
	_, ok := ExtractMarked("no markers here")
	if ok {
		t.Error("expected ok=false")
	}
}

func TestExtractMarked_no_end(t *testing.T) {
	_, ok := ExtractMarked("---RESPONSE---\npartial")
	if ok {
		t.Error("expected ok=false when ---END--- is absent")
	}
}

func TestExtractMarked_empty_body(t *testing.T) {
	body, ok := ExtractMarked("---RESPONSE---\n---END---")
	if !ok {
		t.Fatal("expected ok=true")
	}
	// Empty body between markers returns the empty-response sentinel
	if body != "" {
		t.Errorf("expected empty body, got %q", body)
	}
}

func TestExtractMarked_with_ansi(t *testing.T) {
	// Markers should be found even if surrounding content has ANSI codes
	raw := "\x1b[32m---RESPONSE---\x1b[0m\nthe answer\n---END---"
	body, ok := ExtractMarked(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if body != "the answer" {
		t.Errorf("got %q", body)
	}
}

// ─── ExtractPlain ────────────────────────────────────────────────────────────

func TestExtractPlain_strips_echo(t *testing.T) {
	raw := "[Message from alice]: ping\nHello from claude\n❯ "
	got := ExtractPlain(raw, "[Message from alice]: ping")
	if got == "(no response)" {
		t.Error("expected some response")
	}
	if containsStr(got, "[Message from alice]") {
		t.Error("echo line should be stripped")
	}
}

func TestExtractPlain_strips_status(t *testing.T) {
	raw := "❯ \n⏺ thinking\nActual response\n──────────"
	got := ExtractPlain(raw, "")
	if containsStr(got, "❯") || containsStr(got, "⏺") || containsStr(got, "──") {
		t.Errorf("status lines not stripped: %q", got)
	}
	if !containsStr(got, "Actual response") {
		t.Errorf("response missing: %q", got)
	}
}

func TestExtractPlain_empty(t *testing.T) {
	got := ExtractPlain("❯ \n⏺ \n──────\n", "")
	if got != "(no response)" {
		t.Errorf("expected sentinel, got %q", got)
	}
}

// ─── ShouldAcceptBypass ──────────────────────────────────────────────────────

func TestShouldAcceptBypass_newDialog(t *testing.T) {
	// Current Claude dialog: "Yes, I trust this folder" / "No, exit"
	// (words split by ANSI cursor-positioning codes)
	dialog := "\x1b[2G\x1b[38;5;153m❯\x1b[4G\x1b[38;5;246m1.\x1b[7G\x1b[38;5;153mYes,\x1b[12GI\x1b[14Gtrust\x1b[20Gthis\x1b[25Gfolder\x1b[39m\r\n" +
		"\x1b[4G\x1b[38;5;246m2.\x1b[7G\x1b[39mNo,\x1b[11Gexit"
	if !ShouldAcceptBypass(dialog) {
		t.Error("expected bypass detection in new trust-folder dialog")
	}
}

func TestShouldAcceptBypass_oldDialog(t *testing.T) {
	// Older Claude dialog with "accept" wording
	dialog := "\x1b[38;5;153mNo,\x1b[12Gexit\x1b[39m\r\n\x1b[5G\x1b[38;5;246m2.\x1b[8G\x1b[39mYes,\x1b[13GI\x1b[15Gaccept"
	if !ShouldAcceptBypass(dialog) {
		t.Error("expected bypass detection in old accept dialog")
	}
}

func TestShouldAcceptBypass_false(t *testing.T) {
	if ShouldAcceptBypass("Welcome to Claude Code") {
		t.Error("should not detect bypass on normal startup")
	}
}

// ─── IsPromptReady ───────────────────────────────────────────────────────────

func TestIsPromptReady(t *testing.T) {
	cases := []struct {
		buf  string
		want bool
	}{
		// Interactive prompt: ❯ followed by space (after ANSI strip)
		{"❯ ", true},
		{"some output\n❯ \nmore", true},
		// ANSI codes around ❯ — strip reveals "❯ "
		{"\x1b[39m❯\x1b[0m ", true},
		// Trust dialog: full dialog always has both options, "No," guards the fallback
		{"\x1b[38;5;153m❯\x1b[4G1.\x1b[7GYes,\x1b[12GI\x1b[14Gtrust\x1b[20Gthis\x1b[25Gfolder\x1b[39m\r\n\x1b[4G2.\x1b[7GNo,\x1b[11Gexit", false},
		// Status bar text
		{"bypass permissions on (shift+tab)", true},
		{"⏵⏵ bypass permissions on", true},
		{"still loading...", false},
		{"", false},
	}
	for _, c := range cases {
		got := IsPromptReady(c.buf)
		if got != c.want {
			t.Errorf("IsPromptReady(%q) = %v, want %v", c.buf, got, c.want)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub ||
		len(s) > 0 && func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
