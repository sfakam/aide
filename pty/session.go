package pty

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// claudeMD is written to each session's work directory before starting claude.
const claudeMD = `# Bot Mode

You are running as a messaging bot. Users contact you via Telegram or Webex.
Inbound messages arrive as: [Message from <sender>]: <text>

IMPORTANT — slash commands: if the message text starts with a slash command such as
/sdlc:full-pr-review, /platform-common:setup, or any /plugin:skill-name, execute it
immediately and completely. Do not describe what you are about to do. Just do it, as
if the user had typed it at your interactive prompt.

Rules:
- Reply concisely in plain text.
- For slash commands: complete ALL work the skill requires first, then write a concise
  plain-text summary of results.
`

// Session represents a persistent Claude conversation.
// Each Send call is a subprocess invocation of `claude --print`; conversation
// state is maintained by Claude Code's own session storage via --continue.
type Session struct {
	id         string
	claudePath string
	workDir    string
	log        *slog.Logger

	mu      sync.Mutex // serialises Send calls
	started bool       // true after the first successful Send

	aliveMu  sync.RWMutex
	alive    bool
	lastUsed time.Time
}

// newSession creates a Session but does not start it.
func newSession(id, claudePath, workDir string) *Session {
	return &Session{
		id:         id,
		claudePath: claudePath,
		workDir:    workDir,
		log:        slog.With("session", id),
		lastUsed:   time.Now(),
		alive:      true,
	}
}

// start prepares the session's working directory with CLAUDE.md.
func (s *Session) start(_ context.Context) error {
	return s.ensureWorkDir()
}

// Send runs `claude --print [--continue] <prompt>` and returns the response
// from stdout. Subsequent calls use --continue so Claude Code resumes the same
// conversation thread stored on disk in s.workDir.
func (s *Session) Send(ctx context.Context, sender, text string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.aliveMu.Lock()
	s.lastUsed = time.Now()
	s.aliveMu.Unlock()

	// Give each subprocess up to 5 minutes (enough for long skills like full-pr-review).
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var prompt string
	if cmd := extractSlashCmd(text); cmd != "" {
		prompt = cmd
		s.log.Debug("slash command", "cmd", cmd[:min(80, len(cmd))])
	} else {
		prompt = fmt.Sprintf("[Message from %s]: %s", sender, text)
		s.log.Debug("message", "sender", sender, "text", text[:min(60, len(text))])
	}

	out, err := s.runClaude(ctx, prompt, s.started)
	if err != nil && s.started {
		// --continue can fail if the prior session file was purged from disk.
		s.log.Warn("--continue failed, retrying as new session", "err", err)
		out, err = s.runClaude(ctx, prompt, false)
	}
	if err != nil {
		return "", fmt.Errorf("claude subprocess: %w", err)
	}

	s.started = true
	resp := StripDebugLogs(string(out))
	s.log.Info("response received", "len", len(resp))
	s.log.Debug("response text", "text", resp)
	return resp, nil
}

// IsAlive returns true unless Kill has been called.
func (s *Session) IsAlive() bool {
	s.aliveMu.RLock()
	defer s.aliveMu.RUnlock()
	return s.alive
}

// LastUsed returns the time of the last Send call.
func (s *Session) LastUsed() time.Time {
	s.aliveMu.RLock()
	defer s.aliveMu.RUnlock()
	return s.lastUsed
}

// Kill marks the session as dead so the manager reaps it.
func (s *Session) Kill() {
	s.aliveMu.Lock()
	s.alive = false
	s.aliveMu.Unlock()
}

// ─── Internal ─────────────────────────────────────────────────────────────────

func (s *Session) runClaude(ctx context.Context, prompt string, cont bool) ([]byte, error) {
	args := []string{"--print", "--dangerously-skip-permissions"}
	if cont {
		args = append(args, "--continue")
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, s.claudePath, args...)
	cmd.Dir = s.workDir
	cmd.Env = buildChildEnv()
	s.log.Info("running claude --print", "continue", cont)
	return cmd.Output()
}

func (s *Session) ensureWorkDir() error {
	if err := os.MkdirAll(s.workDir, 0o755); err != nil {
		return fmt.Errorf("create work dir %q: %w", s.workDir, err)
	}
	// Always overwrite so that CLAUDE.md stays in sync with the binary.
	mdPath := s.workDir + "/CLAUDE.md"
	if err := os.WriteFile(mdPath, []byte(claudeMD), 0o644); err != nil {
		return fmt.Errorf("write CLAUDE.md: %w", err)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// buildChildEnv constructs the environment for a spawned Claude Code process.
// It starts from os.Environ() but strips vars that mark the current process as
// a VS Code extension child/sub-session, and vars that produce debug output on
// stdout (ANTHROPIC_LOG=debug pollutes the response captured from --print mode).
func buildChildEnv() []string {
	stripKeys := map[string]bool{
		"CLAUDE_CODE_CHILD_SESSION": true,
		"CLAUDE_CODE_SESSION_ID":    true,
		"ANTHROPIC_LOG":             true, // causes SDK to write HTTP debug logs to stdout
		"DEBUG":                     true,
		"NODE_DEBUG":                true,
	}
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		if !stripKeys[key] {
			env = append(env, e)
		}
	}
	return env
}

// extractSlashCmd detects a slash command in a Webex/Telegram message and
// returns it as a single normalized line, or "".
func extractSlashCmd(text string) string {
	tokens := strings.Fields(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(text))
	start := -1
	for i, t := range tokens {
		if strings.HasPrefix(t, "/") && !strings.Contains(t, "://") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	return strings.Join(tokens[start:], " ")
}
