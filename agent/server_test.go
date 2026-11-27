package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/sfathall/aide/channels"
	"github.com/sfathall/aide/config"
	"github.com/sfathall/aide/store"
)

var errSendFailed = errors.New("send failed")

// stubChannel is a minimal channels.Channel for testing /messages without
// touching a real messaging platform.
type stubChannel struct {
	id      string
	sent    []stubSend
	sendErr error
}

type stubSend struct {
	roomID, text string
}

func (c *stubChannel) ID() string { return c.id }
func (c *stubChannel) Start(ctx context.Context, inbound chan<- channels.InboundMessage) error {
	<-ctx.Done()
	return nil
}
func (c *stubChannel) Send(ctx context.Context, roomID, text string) error {
	if c.sendErr != nil {
		return c.sendErr
	}
	c.sent = append(c.sent, stubSend{roomID, text})
	return nil
}
func (c *stubChannel) LoadCursor(cursor string) {}
func (c *stubChannel) SaveCursor() string       { return "" }

func newTestServer(t *testing.T, cfg *config.Config, chs map[string]channels.Channel) (*Server, *httptest.Server) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := New("", cfg, db, fakeClaudeCLI(t), chs, NewHeartbeatTracker())
	ts := httptest.NewServer(s.srv.Handler)
	t.Cleanup(ts.Close)
	return s, ts
}

func testAgentConfig() *config.Config {
	return &config.Config{
		Agents: []config.Agent{
			{
				ID:                        "a1",
				Token:                     "secret-token",
				RateLimit:                 config.AgentRateLimit{MaxRequests: 100, WindowMinutes: 60},
				MaxTurnsPerSession:        3,
				MaxSessionDurationMinutes: 480,
				IdleTimeoutMinutes:        1440,
				ChannelsAllowed: []config.AgentChannelRule{
					{ChannelID: "wx", RoomIDs: []string{"room1"}},
				},
			},
		},
	}
}

func doInfer(t *testing.T, ts *httptest.Server, agentID, token string, body map[string]any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/agents/"+agentID+"/infer", bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func TestHandleInfer_success(t *testing.T) {
	_, ts := newTestServer(t, testAgentConfig(), nil)
	resp := doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}
	var out inferResponse
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Response != "OK" {
		t.Errorf("got response %q", out.Response)
	}
	if out.ClaudeSessionID == "" {
		t.Error("expected a claude_session_id")
	}
}

func TestHandleInfer_unknownAgent(t *testing.T) {
	_, ts := newTestServer(t, testAgentConfig(), nil)
	resp := doInfer(t, ts, "nope", "secret-token", map[string]any{"prompt": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("got status %d, want 404", resp.StatusCode)
	}
}

func TestHandleInfer_badToken(t *testing.T) {
	_, ts := newTestServer(t, testAgentConfig(), nil)
	resp := doInfer(t, ts, "a1", "wrong-token", map[string]any{"prompt": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got status %d, want 401", resp.StatusCode)
	}
}

func TestHandleInfer_missingPrompt(t *testing.T) {
	_, ts := newTestServer(t, testAgentConfig(), nil)
	resp := doInfer(t, ts, "a1", "secret-token", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", resp.StatusCode)
	}
}

func TestHandleInfer_modelNotAllowed(t *testing.T) {
	cfg := testAgentConfig()
	cfg.Agents[0].ModelsAllowed = []string{"claude-haiku-4-5"}
	_, ts := newTestServer(t, cfg, nil)
	resp := doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "hi", "model": "claude-sonnet-5"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("got status %d, want 403", resp.StatusCode)
	}
}

func TestHandleInfer_rateLimited(t *testing.T) {
	cfg := testAgentConfig()
	cfg.Agents[0].RateLimit = config.AgentRateLimit{MaxRequests: 1, WindowMinutes: 60}
	_, ts := newTestServer(t, cfg, nil)
	doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "hi"}).Body.Close()
	resp := doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("got status %d, want 429", resp.StatusCode)
	}
}

func TestHandleInfer_sessionContinuity(t *testing.T) {
	_, ts := newTestServer(t, testAgentConfig(), nil)

	resp1 := doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "remember 42", "session_id": "main"})
	var out1 inferResponse
	json.NewDecoder(resp1.Body).Decode(&out1)
	resp1.Body.Close()

	resp2 := doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "what number?", "session_id": "main"})
	var out2 inferResponse
	json.NewDecoder(resp2.Body).Decode(&out2)
	resp2.Body.Close()

	if out2.ClaudeSessionID != out1.ClaudeSessionID {
		t.Errorf("expected same underlying session across calls, got %q then %q", out1.ClaudeSessionID, out2.ClaudeSessionID)
	}
	if out2.Compacted {
		t.Error("second call should not have triggered compaction")
	}
}

func TestHandleInfer_rotatesAfterMaxTurns(t *testing.T) {
	cfg := testAgentConfig()
	cfg.Agents[0].MaxTurnsPerSession = 2
	_, ts := newTestServer(t, cfg, nil)

	var last inferResponse
	for i := 0; i < 3; i++ {
		resp := doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "hi", "session_id": "main"})
		json.NewDecoder(resp.Body).Decode(&last)
		resp.Body.Close()
	}
	if !last.Compacted {
		t.Error("expected the 3rd call to trigger rotation (compacted=true)")
	}
}

func TestHandleInfer_budgetExhaustedSurfacesAsSuccessfulResponse(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/claude"
	script := "#!/bin/sh\n" +
		`echo '{"type":"result","subtype":"error_max_budget_usd","is_error":true,"session_id":"s1","total_cost_usd":2.4}'` + "\n" +
		"exit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := New("", testAgentConfig(), db, path, nil, NewHeartbeatTracker())
	ts := httptest.NewServer(s.srv.Handler)
	t.Cleanup(ts.Close)

	resp := doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "hi", "max_budget_usd": 2})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("budget-exhausted should still be a 200 with usage attached, got %d", resp.StatusCode)
	}
	var out inferResponse
	json.NewDecoder(resp.Body).Decode(&out)
	if !out.IsError || out.ErrorSubtype != "error_max_budget_usd" {
		t.Errorf("got %+v", out)
	}
	if out.Usage.CostUSD != 2.4 {
		t.Errorf("expected usage to be attached, got %+v", out.Usage)
	}
}

func TestHandleInfer_timeoutSecsOverride(t *testing.T) {
	// A sleeping fake claude: with the default 5-minute timeout this would
	// hang the test; timeout_secs=1 should cut it off quickly instead.
	dir := t.TempDir()
	path := dir + "/claude"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := New("", testAgentConfig(), db, path, nil, NewHeartbeatTracker())
	ts := httptest.NewServer(s.srv.Handler)
	t.Cleanup(ts.Close)

	start := time.Now()
	resp := doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "hi", "timeout_secs": 1})
	defer resp.Body.Close()
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout_secs override not applied: took %s", elapsed)
	}
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Errorf("got status %d, want 408", resp.StatusCode)
	}
}

func TestHandleInfer_ephemeralSessionNotPersisted(t *testing.T) {
	s, ts := newTestServer(t, testAgentConfig(), nil)
	resp := doInfer(t, ts, "a1", "secret-token", map[string]any{"prompt": "hi"})
	resp.Body.Close()

	got, err := s.db.GetAgentSession("a1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected no session row to be persisted for an omitted session_id")
	}
}

// ─── /messages ──────────────────────────────────────────────────────────────

func doMessages(t *testing.T, ts *httptest.Server, agentID, token string, body map[string]any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/agents/"+agentID+"/messages", bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func TestHandleMessages_success(t *testing.T) {
	ch := &stubChannel{id: "wx"}
	_, ts := newTestServer(t, testAgentConfig(), map[string]channels.Channel{"wx": ch})
	resp := doMessages(t, ts, "a1", "secret-token", map[string]any{"channel_id": "wx", "room_id": "room1", "text": "hello"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}
	if len(ch.sent) != 1 || ch.sent[0].roomID != "room1" || ch.sent[0].text != "hello" {
		t.Errorf("unexpected sends: %+v", ch.sent)
	}
}

func TestHandleMessages_roomNotAllowlisted(t *testing.T) {
	ch := &stubChannel{id: "wx"}
	_, ts := newTestServer(t, testAgentConfig(), map[string]channels.Channel{"wx": ch})
	resp := doMessages(t, ts, "a1", "secret-token", map[string]any{"channel_id": "wx", "room_id": "other-room", "text": "hello"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("got status %d, want 403", resp.StatusCode)
	}
	if len(ch.sent) != 0 {
		t.Error("expected no message to be sent")
	}
}

func TestHandleMessages_sendFailure(t *testing.T) {
	ch := &stubChannel{id: "wx", sendErr: errSendFailed}
	_, ts := newTestServer(t, testAgentConfig(), map[string]channels.Channel{"wx": ch})
	resp := doMessages(t, ts, "a1", "secret-token", map[string]any{"channel_id": "wx", "room_id": "room1", "text": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("got status %d, want 502", resp.StatusCode)
	}
}

// ─── /heartbeat ─────────────────────────────────────────────────────────────

func TestHandleHeartbeat_recordsTouch(t *testing.T) {
	s, ts := newTestServer(t, testAgentConfig(), nil)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/agents/a1/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}
	if _, ok := s.hb.LastSeen("a1"); !ok {
		t.Error("expected heartbeat to be recorded")
	}
}

func TestHandleHeartbeat_badToken(t *testing.T) {
	_, ts := newTestServer(t, testAgentConfig(), nil)
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/agents/a1/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got status %d, want 401", resp.StatusCode)
	}
}

func TestHandleMessages_unknownChannel(t *testing.T) {
	cfg := testAgentConfig()
	cfg.Agents[0].ChannelsAllowed = []config.AgentChannelRule{{ChannelID: "ghost"}}
	_, ts := newTestServer(t, cfg, map[string]channels.Channel{})
	resp := doMessages(t, ts, "a1", "secret-token", map[string]any{"channel_id": "ghost", "room_id": "r", "text": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("got status %d, want 404", resp.StatusCode)
	}
}
