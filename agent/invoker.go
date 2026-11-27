package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ModelUsage is the per-model token/cost breakdown from the result object's
// "modelUsage" field. It covers the main session and any subagents.
type ModelUsage struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	CostUSD                  float64
}

// Usage is what Claude Code's /usage would report for one inference call.
type Usage struct {
	CostUSD       float64
	DurationMs    int64
	APIDurationMs int64
	NumTurns      int
	Subagents     int
	Models        map[string]ModelUsage
}

// IsZero reports whether no usage was captured (e.g. the CLI produced no
// result object at all — see InferResult.IsError for the common case of a
// result object that reports an error).
func (u Usage) IsZero() bool {
	return u.CostUSD == 0 && u.NumTurns == 0 && len(u.Models) == 0
}

// InferResult is the subset of `claude --output-format json`'s result object
// that aide-agents needs. Verified against claude-cli 2.1.332; see
// cliResult below for the exact fields read off the wire.
type InferResult struct {
	Response        string
	ClaudeSessionID string
	InputTokens     int
	OutputTokens    int
	Usage           Usage

	// IsError and the fields below it are set when claude's result object
	// itself reports an error (e.g. a budget cap was reached) — this is not
	// a Go error: the call still produced a usable result object, usage is
	// still meaningful, and the caller is expected to check IsError rather
	// than the method's error return, which is reserved for cases with no
	// usable result object at all (process failed to start, output
	// unparseable, ...).
	IsError      bool
	ErrorSubtype string // e.g. "error_max_budget_usd"
	Errors       []string
}

// BudgetExhausted reports whether this result stopped because of
// InferOptions.MaxBudgetUSD.
func (r *InferResult) BudgetExhausted() bool {
	return r.IsError && r.ErrorSubtype == "error_max_budget_usd"
}

// InferOptions are the parameters of one Infer call. Prompt is required;
// everything else is optional (zero value = flag omitted).
type InferOptions struct {
	Prompt          string
	ResumeSessionID string
	Model           string
	Effort          string

	// AppendSystemPrompt and MaxBudgetUSD only ever narrow what an inference
	// call can do relative to the default (more instructions, a spend cap),
	// never widen it, so unlike Model/Effort these aren't gated by an
	// agent-level allowlist.
	AppendSystemPrompt string
	MaxBudgetUSD       float64

	// AllowedTools, when non-empty, locks the run down instead of using
	// aide's default permission handling: only user settings are loaded
	// (never a checkout's project/local settings), nothing can prompt
	// (anything not allowed is denied), and these rules are passed as
	// --allowedTools / --disallowedTools. Leave empty to keep aide's
	// existing default behavior unchanged.
	AllowedTools    []string
	DisallowedTools []string

	// WorkDir, if set, is the directory claude runs in (cmd.Dir) — e.g. a
	// git checkout the agent prepared so Bash(git diff:*) and friends see
	// the right repo. Aide doesn't restrict it to any prefix: aide and its
	// registered agents already run as the same OS user, so this only
	// changes where the equally-privileged claude process runs from, not
	// what it can reach. Empty means "wherever aide's own process runs
	// from" (aide's previous, unconditional behavior).
	WorkDir string
}

// Invoker runs the Claude CLI non-interactively, once per call. Agent sessions
// have no persistent process (see aide-agents/docs/design.md §5) — continuity
// comes entirely from passing the previous claude_session_id to --resume.
type Invoker struct {
	ClaudePath string
}

// Infer runs one prompt. The returned error is non-nil only when there's no
// usable result object at all (claude failed to start, or its output could
// not be parsed); a result object that itself reports an error (e.g. a
// budget cap reached) is returned normally with InferResult.IsError set.
func (inv *Invoker) Infer(ctx context.Context, opts InferOptions) (*InferResult, error) {
	args := []string{"--print", opts.Prompt, "--output-format", "json"}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	if opts.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystemPrompt)
	}
	if opts.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(opts.MaxBudgetUSD, 'f', -1, 64))
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args,
			"--setting-sources", "user",
			"--permission-prompts", "none",
			"--allowedTools", strings.Join(opts.AllowedTools, ","))
		if len(opts.DisallowedTools) > 0 {
			args = append(args, "--disallowedTools", strings.Join(opts.DisallowedTools, ","))
		}
	}

	claudePath := inv.ClaudePath
	if claudePath == "" {
		claudePath = "claude"
	}
	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = opts.WorkDir
	// On context cancellation, exec.CommandContext's default Cancel only
	// kills the direct claude process — a grandchild it spawned (an MCP
	// server, a tool subprocess) can inherit the stdout/stderr pipes and
	// keep them open after claude itself is dead, which would otherwise
	// hang Run() until that grandchild exits on its own, timeout or not.
	// WaitDelay bounds how long Wait() waits for the pipes to drain after
	// cancellation before forcibly closing them. Caught by
	// TestHandleInfer_timeoutSecsOverride hanging well past its deadline.
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Debug/verbose logging some environments enable (e.g. ANTHROPIC_LOG) can
	// still land on stdout ahead of the final JSON result line — observed
	// directly against this CLI version even with the var unset for the
	// child process. We never trust "stdout == JSON"; see parseResult.
	cmd.Env = append(cmd.Environ(), "ANTHROPIC_LOG=")

	runErr := cmd.Run()

	// Always try to parse a result object first, even when the process
	// exited non-zero: claude can exit non-zero while still reporting a
	// structured error (e.g. a budget cap) in its last JSON line, and that's
	// the more useful signal — it carries real usage, not just an exit code.
	result, parseErr := parseResult(stdout.Bytes())
	if parseErr == nil {
		return result, nil
	}
	if runErr != nil {
		return nil, fmt.Errorf("claude invocation failed: %w (stderr: %s)", runErr, strings.TrimSpace(stderr.String()))
	}
	return nil, parseErr
}

// cliModelUsage mirrors one entry of the result object's "modelUsage" map.
type cliModelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

// cliResult mirrors the fields we read from `claude --output-format json`'s
// final line. The CLI emits other fields aide-agents doesn't need; they're
// ignored.
type cliResult struct {
	Type          string                   `json:"type"`
	Subtype       string                   `json:"subtype"`
	Result        string                   `json:"result"`
	SessionID     string                   `json:"session_id"`
	IsError       bool                     `json:"is_error"`
	Errors        []string                 `json:"errors"`
	TotalCostUSD  float64                  `json:"total_cost_usd"`
	DurationMs    int64                    `json:"duration_ms"`
	DurationAPIMs int64                    `json:"duration_api_ms"`
	NumTurns      int                      `json:"num_turns"`
	ModelUsage    map[string]cliModelUsage `json:"modelUsage"`
	SubagentStats struct {
		Spawned int `json:"spawned"`
	} `json:"subagent_stats"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// parseResult extracts the JSON result object from the CLI's stdout.
// It scans from the last line backward for the first line that parses as a
// cliResult with type "result", rather than assuming the whole of stdout is
// a single JSON document — verbose/debug output from the CLI or its
// dependencies can precede the actual result line.
func parseResult(stdout []byte) (*InferResult, error) {
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || line[0] != '{' {
			continue
		}
		var r cliResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if r.Type != "result" || r.SessionID == "" {
			continue
		}
		models := make(map[string]ModelUsage, len(r.ModelUsage))
		for name, m := range r.ModelUsage {
			models[name] = ModelUsage{
				InputTokens:              m.InputTokens,
				OutputTokens:             m.OutputTokens,
				CacheReadInputTokens:     m.CacheReadInputTokens,
				CacheCreationInputTokens: m.CacheCreationInputTokens,
				CostUSD:                  m.CostUSD,
			}
		}
		return &InferResult{
			Response:        r.Result,
			ClaudeSessionID: r.SessionID,
			InputTokens:     r.Usage.InputTokens,
			OutputTokens:    r.Usage.OutputTokens,
			Usage: Usage{
				CostUSD:       r.TotalCostUSD,
				DurationMs:    r.DurationMs,
				APIDurationMs: r.DurationAPIMs,
				NumTurns:      r.NumTurns,
				Subagents:     r.SubagentStats.Spawned,
				Models:        models,
			},
			IsError:      r.IsError,
			ErrorSubtype: r.Subtype,
			Errors:       r.Errors,
		}, nil
	}
	return nil, fmt.Errorf("no result line found in claude output (%d bytes)", len(stdout))
}
