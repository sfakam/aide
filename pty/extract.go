// Package pty manages persistent Claude CLI sessions via pseudo-terminals.
// This file contains pure extraction functions with no I/O — fully unit-testable.
package pty

import (
	"regexp"
	"strings"
)

// ─── ANSI ─────────────────────────────────────────────────────────────────────

var ansiRE = regexp.MustCompile(
	// OSC must come before the generic single-char ESC alternative because ']'
	// falls in the \-_ range and would otherwise be consumed as a 2-byte sequence.
	`(?:\x1B\][^\x07\x1B]*(?:\x07|\x1B\\)|\x1B\[[0-?]*[ -/]*[@-~]|\x1B[@-Z\\-_])`,
)

// cursorMoveRE matches CSI cursor-movement sequences that position the cursor
// to an absolute column or move it forward (e.g. \x1b[12G, \x1b[4C).
// These are replaced with a space in text extraction so word boundaries are
// preserved — Claude renders with column jumps instead of literal spaces.
var cursorMoveRE = regexp.MustCompile(`\x1B\[[0-9;]*[ABCDG]`)

// multiSpaceRE collapses runs of spaces produced by cursor-move substitution.
var multiSpaceRE = regexp.MustCompile(` {2,}`)

// StripANSI removes ANSI escape sequences from s.
func StripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// stripANSIText is like StripANSI but replaces cursor-movement codes with a
// space first, so words separated by column-jump codes aren't concatenated.
func stripANSIText(s string) string {
	s = cursorMoveRE.ReplaceAllString(s, " ")
	s = ansiRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = multiSpaceRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// ─── Response extraction ──────────────────────────────────────────────────────

const (
	markerStart = "---RESPONSE---"
	markerEnd   = "---END---"
)

// ExtractMarked returns the text between ---RESPONSE--- and ---END--- markers,
// or ("", false) if the markers are not both present.
func ExtractMarked(raw string) (string, bool) {
	start := strings.Index(raw, markerStart)
	if start < 0 {
		return "", false
	}
	end := strings.Index(raw[start:], markerEnd)
	if end < 0 {
		return "", false
	}
	body := raw[start+len(markerStart) : start+end]
	body = stripANSIText(body)
	return body, true
}

// ExtractPlain is a best-effort fallback when markers are absent.
// It strips ANSI codes, removes the echoed input line and known status lines,
// and returns whatever text remains.
func ExtractPlain(raw, echoedInput string) string {
	clean := StripANSI(raw)
	clean = strings.ReplaceAll(clean, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "\n")

	var out []string
	for _, ln := range strings.Split(clean, "\n") {
		s := strings.TrimRight(ln, " \t")
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		// Skip the echoed input line.
		// Cursor-movement ANSI codes replace spaces in the raw PTY stream, so
		// after StripANSI the echoed line has no spaces ("Messagefromuser:text").
		// Normalize whitespace on both sides before comparing.
		if echoedInput != "" {
			normLine := strings.Join(strings.Fields(trimmed), "")
			normEcho := strings.Join(strings.Fields(echoedInput), "")
			if strings.Contains(normLine, normEcho) {
				continue
			}
		}
		// Skip claude TUI status / spinner lines
		if isStatusLine(trimmed) {
			continue
		}
		out = append(out, s)
	}
	// Trim leading/trailing blank entries
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return "(no response)"
	}
	return strings.Join(out, "\n")
}

func isStatusLine(s string) bool {
	// Claude TUI prompt, spinner, and tool-output characters.
	// ⎿ = hook/tool result prefix; ⧉ = file reference; ⏺/⏵/⏸ = spinner states.
	statusPrefixes := []string{"❯", "⏺", "⏵", "⏸", "⚠", "●", "◇", "◒", "✓", "✗", "⎿", "⧉"}
	for _, p := range statusPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	// Separator lines (box-drawing chars)
	if len(s) > 4 {
		allSep := true
		for _, r := range s {
			if r != '─' && r != '━' && r != '═' && r != '╌' && r != '┄' && r != '-' {
				allSep = false
				break
			}
		}
		if allSep {
			return true
		}
	}
	return false
}

// ExtractMenu detects when Claude Code is showing an interactive numbered-option
// menu and returns the formatted menu text. Returns ("", false) if no menu is active.
//
// The caller should relay the menu back to the user so they can reply with the
// option number (e.g. "1"), which is then submitted to the PTY as the next message.
//
// Detection key: "❯ 1." — the selection cursor positioned on the first numbered
// option. This is distinct from the regular interactive prompt "❯ " (with trailing
// space and no digit) and from the trust dialog "❯1." (no space).
func ExtractMenu(raw string) (string, bool) {
	text := stripANSIText(raw)
	if !strings.Contains(text, "❯ 1.") {
		return "", false
	}
	// "esc to interrupt" is present while Claude is mid-turn (spinner running).
	// Only match when it's absent — i.e. Claude is genuinely waiting for input.
	if strings.Contains(strings.ToLower(text), "esc to interrupt") {
		return "", false
	}
	lines := strings.Split(text, "\n")
	menuLine := -1
	for i, ln := range lines {
		if strings.Contains(ln, "❯ 1.") {
			menuLine = i
			break
		}
	}
	if menuLine < 0 {
		return "", false
	}
	// Include up to 8 lines before the first option for the question/preamble.
	start := max(0, menuLine-8)
	var out []string
	for i := start; i < len(lines) && i < menuLine+20; i++ {
		trimmed := strings.TrimRight(lines[i], " \t")
		// Stop at a blank line that follows at least 2 option lines past the cursor.
		if strings.TrimSpace(trimmed) == "" && i > menuLine+2 {
			break
		}
		if strings.TrimSpace(trimmed) != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return "", false
	}
	return strings.Join(out, "\n"), true
}

// ─── Subprocess output cleaning ──────────────────────────────────────────────

var (
	logLineRE      = regexp.MustCompile(`^\[log_[0-9a-f]+\]`)
	responseLineRE = regexp.MustCompile(`^response \d+ https?://`)
)

// StripDebugLogs removes Anthropic SDK debug log blocks from claude --print
// stdout. These appear when ANTHROPIC_LOG=debug is set (e.g. via settings.json)
// and always precede the actual response text.
//
// Two patterns are removed:
//   - [log_XXXXXX] single-line entries
//   - [log_XXXXXX] multi-line blocks starting with { and ending with }
//   - "response NNN https://..." HTTP summary lines
func StripDebugLogs(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	depth := 0
	inBlock := false

	for _, line := range lines {
		if inBlock {
			for _, c := range line {
				if c == '{' {
					depth++
				} else if c == '}' {
					depth--
				}
			}
			if depth <= 0 {
				inBlock = false
				depth = 0
			}
			continue
		}
		if logLineRE.MatchString(line) || responseLineRE.MatchString(line) {
			opens := strings.Count(line, "{")
			closes := strings.Count(line, "}")
			if opens > closes {
				inBlock = true
				depth = opens - closes
			}
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// ─── Startup detection ────────────────────────────────────────────────────────

// ShouldAcceptBypass returns true when the accumulated PTY output contains
// the trust/bypass confirmation dialog shown by Claude on first run in a directory.
// ANSI codes are stripped before matching because cursor-positioning escapes
// are injected between every word — the visual spaces on screen are column jumps
// (\x1b[NG), not literal space bytes, so multi-word phrases are never contiguous.
func ShouldAcceptBypass(buf string) bool {
	stripped := StripANSI(buf)
	// Current Claude dialog: "Yes, I trust this folder" / "No, exit"
	// "trust" appears in the question preamble or option 1; "No," only appears when
	// the option list is rendered, so together they uniquely identify the dialog.
	if strings.Contains(stripped, "trust") && strings.Contains(stripped, "No,") {
		return true
	}
	// Older Claude dialog: "Yes, I accept" / "No, exit"
	if strings.Contains(stripped, "No,") && strings.Contains(stripped, "accept") {
		return true
	}
	return false
}

// IsPromptReady returns true when the accumulated PTY output indicates that
// the Claude interactive prompt is visible and ready for input.
// ANSI codes are stripped before checking because Claude positions ❯ with
// cursor-movement sequences rather than trailing the character with a space
// in raw bytes (e.g. ❯\x1b[4G1. in the selection dialog vs ❯ in the prompt).
func IsPromptReady(buf string) bool {
	stripped := StripANSI(buf)
	// "❯ " (U+276F + space) is Claude's interactive input prompt.
	// The trust-dialog selection cursor renders as "❯1." (no space), so this
	// correctly distinguishes prompt-ready from the dialog state.
	if strings.Contains(stripped, "❯ ") {
		return true
	}
	// Fallback: ❯ anywhere in the buffer that is NOT the trust-dialog selector.
	// The dialog always has "No," when showing the option list; the interactive
	// prompt does not.
	if strings.Contains(stripped, "❯") && !strings.Contains(stripped, "No,") {
		return true
	}
	// Status bar text visible only after the interactive session loads.
	if strings.Contains(strings.ToLower(buf), "bypass permissions on") {
		return true
	}
	return false
}
