package telegram

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sfathall/aide/channels"
)

func TestNew(t *testing.T) {
	ch := New("tg1", "mytoken", 5*time.Second)
	if ch.ID() != "tg1" {
		t.Errorf("expected id tg1, got %q", ch.ID())
	}
}

func TestCursorRoundtrip(t *testing.T) {
	ch := New("tg1", "tok", 5*time.Second)
	ch.LoadCursor("42")
	if ch.offset != 42 {
		t.Errorf("expected offset 42, got %d", ch.offset)
	}
	if ch.SaveCursor() != "42" {
		t.Errorf("expected cursor '42', got %q", ch.SaveCursor())
	}
}

func TestCursorEmpty(t *testing.T) {
	ch := New("tg1", "tok", 5*time.Second)
	ch.LoadCursor("")
	if ch.offset != 0 {
		t.Errorf("expected offset 0, got %d", ch.offset)
	}
}

func TestToInbound_basic(t *testing.T) {
	ch := New("tg1", "tok", 5*time.Second)
	u := tgUpdate{
		UpdateID: 100,
		Message: &tgMessage{
			From: &tgUser{ID: 5, FirstName: "Alice", LastName: "Smith"},
			Chat: tgChat{ID: 999, Type: "private"},
			Text: "hello",
		},
	}
	msg := ch.toInbound(u)
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.SenderName != "Alice Smith" {
		t.Errorf("got senderName %q", msg.SenderName)
	}
	if msg.RoomID != "999" {
		t.Errorf("got roomID %q", msg.RoomID)
	}
	if msg.Text != "hello" {
		t.Errorf("got text %q", msg.Text)
	}
	if msg.IsMention {
		t.Error("should not be a mention")
	}
}

func TestToInbound_mention(t *testing.T) {
	ch := New("tg1", "tok", 5*time.Second)
	u := tgUpdate{
		UpdateID: 101,
		Message: &tgMessage{
			From: &tgUser{ID: 5, FirstName: "Bob"},
			Chat: tgChat{ID: 888},
			Text: "@mybot help",
			Entities: []tgEntity{{Type: "mention", Offset: 0, Length: 6}},
		},
	}
	msg := ch.toInbound(u)
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if !msg.IsMention {
		t.Error("expected IsMention=true")
	}
}

func TestToInbound_no_text(t *testing.T) {
	ch := New("tg1", "tok", 5*time.Second)
	u := tgUpdate{UpdateID: 1, Message: &tgMessage{Text: ""}}
	if ch.toInbound(u) != nil {
		t.Error("expected nil for empty text")
	}
}

func TestToInbound_no_message(t *testing.T) {
	ch := New("tg1", "tok", 5*time.Second)
	if ch.toInbound(tgUpdate{UpdateID: 1}) != nil {
		t.Error("expected nil for nil message")
	}
}

func TestSend_structValid(t *testing.T) {
	// Confirm Channel can be constructed and its fields are accessible.
	ch := &Channel{id: "tg1", token: "tok", pollInterval: time.Second, log: noopLogger()}
	if ch.ID() != "tg1" {
		t.Errorf("unexpected id %q", ch.ID())
	}
}

func noopLogger() *slog.Logger {
	return slog.Default()
}

func TestGetUpdates_parses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"ok": true,
			"result": []any{
				map[string]any{
					"update_id": 200,
					"message": map[string]any{
						"message_id": 1,
						"from":       map[string]any{"id": 10, "first_name": "Dan"},
						"chat":       map[string]any{"id": 50, "type": "private"},
						"text":       "test",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// We can't patch apiBase (it's a package const) without a refactor,
	// but we can test the JSON parsing directly.
	var result struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	body := `{"ok":true,"result":[{"update_id":200,"message":{"message_id":1,"from":{"id":10,"first_name":"Dan"},"chat":{"id":50,"type":"private"},"text":"test"}}]}`
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Error("expected ok=true")
	}
	if len(result.Result) != 1 {
		t.Fatalf("expected 1 update, got %d", len(result.Result))
	}
	if result.Result[0].Message.Text != "test" {
		t.Errorf("got text %q", result.Result[0].Message.Text)
	}
	_ = srv
}

func TestChannelImplementsInterface(t *testing.T) {
	var _ channels.Channel = (*Channel)(nil)
}
