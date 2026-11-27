package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaudeCLI writes an executable shell script that mimics `claude
// --output-format json`'s behavior closely enough to exercise Invoker: it
// echoes some noise lines first (as real debug/verbose logging can do, per
// docs/design.md's note in invoker.go), then the JSON result line last. If
// invoked with --resume <id>, it echoes that id back as session_id;
// otherwise it emits a fixed new session id.
func fakeClaudeCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
echo "[log_noise] sending request {"
echo "  some: debug output"
echo "}"
SESSION_ID="new-session-id"
PREV=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --resume) PREV="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ -n "$PREV" ]; then
  SESSION_ID="$PREV"
fi
echo "{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"OK\",\"session_id\":\"$SESSION_ID\",\"is_error\":false,\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func TestInvoker_Infer_freshSession(t *testing.T) {
	inv := &Invoker{ClaudePath: fakeClaudeCLI(t)}
	res, err := inv.Infer(context.Background(), InferOptions{Prompt: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Response != "OK" {
		t.Errorf("got response %q", res.Response)
	}
	if res.ClaudeSessionID != "new-session-id" {
		t.Errorf("got session id %q", res.ClaudeSessionID)
	}
	if res.InputTokens != 10 || res.OutputTokens != 2 {
		t.Errorf("unexpected usage: %+v", res)
	}
}

func TestInvoker_Infer_resumesSession(t *testing.T) {
	inv := &Invoker{ClaudePath: fakeClaudeCLI(t)}
	res, err := inv.Infer(context.Background(), InferOptions{Prompt: "hello again", ResumeSessionID: "existing-session-id"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ClaudeSessionID != "existing-session-id" {
		t.Errorf("expected resumed session id to be echoed back, got %q", res.ClaudeSessionID)
	}
}

func TestInvoker_Infer_commandFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	os.WriteFile(path, []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755)
	inv := &Invoker{ClaudePath: path}
	_, err := inv.Infer(context.Background(), InferOptions{Prompt: "hello"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected stderr to be included in error, got: %v", err)
	}
}

func TestParseResult_ignoresNoiseBeforeJSON(t *testing.T) {
	stdout := []byte("some debug noise\nmore noise {not json\n" +
		`{"type":"result","result":"hi","session_id":"abc","usage":{"input_tokens":1,"output_tokens":1}}` + "\n")
	res, err := parseResult(stdout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Response != "hi" || res.ClaudeSessionID != "abc" {
		t.Errorf("got %+v", res)
	}
}

func TestParseResult_noResultLine(t *testing.T) {
	_, err := parseResult([]byte("just some noise\nno json here\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestInvoker_Infer_budgetExhaustedIsNotAGoError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
echo '{"type":"result","subtype":"error_max_budget_usd","is_error":true,"session_id":"s1","total_cost_usd":2.4,"errors":["Reached maximum budget ($2)"]}'
exit 1
`
	os.WriteFile(path, []byte(script), 0o755)
	inv := &Invoker{ClaudePath: path}
	res, err := inv.Infer(context.Background(), InferOptions{Prompt: "hello", MaxBudgetUSD: 2})
	if err != nil {
		t.Fatalf("a structured result object should not surface as a Go error: %v", err)
	}
	if !res.BudgetExhausted() {
		t.Fatalf("expected BudgetExhausted, got %+v", res)
	}
	if res.Usage.CostUSD != 2.4 {
		t.Errorf("expected usage to still be captured, got %+v", res.Usage)
	}
	if len(res.Errors) != 1 || res.Errors[0] != "Reached maximum budget ($2)" {
		t.Errorf("errors = %v", res.Errors)
	}
}

func TestInvoker_Infer_runsInWorkDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	workDir := t.TempDir()
	script := `#!/bin/sh
echo "{\"type\":\"result\",\"result\":\"$(pwd)\",\"session_id\":\"s1\"}"
`
	os.WriteFile(path, []byte(script), 0o755)
	inv := &Invoker{ClaudePath: path}
	res, err := inv.Infer(context.Background(), InferOptions{Prompt: "hi", WorkDir: workDir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Resolve symlinks (e.g. /tmp -> /private/tmp on macOS) before comparing.
	wantDir, _ := filepath.EvalSymlinks(workDir)
	gotDir, _ := filepath.EvalSymlinks(res.Response)
	if gotDir != wantDir {
		t.Errorf("claude ran in %q, want %q", res.Response, workDir)
	}
}

func TestInvoker_Infer_passesNewOptions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	argsFile := filepath.Join(dir, "args")
	script := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > ` + argsFile + `
echo '{"type":"result","result":"ok","session_id":"s1"}'
`
	os.WriteFile(path, []byte(script), 0o755)
	inv := &Invoker{ClaudePath: path}
	_, err := inv.Infer(context.Background(), InferOptions{
		Prompt:             "review this",
		AppendSystemPrompt: "rules",
		MaxBudgetUSD:       4,
		AllowedTools:       []string{"Bash(git diff:*)", "Skill"},
		DisallowedTools:    []string{"Write"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(b)
	for _, want := range []string{
		"--append-system-prompt\nrules",
		"--max-budget-usd\n4",
		"--setting-sources\nuser",
		"--permission-prompts\nnone",
		"--allowedTools\nBash(git diff:*),Skill",
		"--disallowedTools\nWrite",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q, got:\n%s", want, args)
		}
	}
}
