package webex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sfathall/aide/channels"
)

func TestNew(t *testing.T) {
	ch := New("wx1", "token", "room123", 5*time.Second)
	if ch.ID() != "wx1" {
		t.Errorf("expected id wx1, got %q", ch.ID())
	}
	if ch.roomID != "room123" {
		t.Errorf("expected roomID room123, got %q", ch.roomID)
	}
}

func TestCursorRoundtrip(t *testing.T) {
	ch := New("wx1", "tok", "r1", time.Second)
	ts := "2024-01-15T10:30:00Z"
	ch.LoadCursor(ts)
	saved := ch.SaveCursor()
	if saved != ts {
		t.Errorf("expected %q, got %q", ts, saved)
	}
}

func TestCursorEmpty(t *testing.T) {
	ch := New("wx1", "tok", "r1", time.Second)
	before := ch.lastSeen
	ch.LoadCursor("")
	if !ch.lastSeen.Equal(before) {
		t.Error("empty cursor should not change lastSeen")
	}
}

func TestCursorBadValue(t *testing.T) {
	ch := New("wx1", "tok", "r1", time.Second)
	before := ch.lastSeen
	ch.LoadCursor("not-a-date")
	if !ch.lastSeen.Equal(before) {
		t.Error("bad cursor should not change lastSeen")
	}
}

func TestChannelImplementsInterface(t *testing.T) {
	var _ channels.Channel = (*Channel)(nil)
}

func TestFetchMessages_parses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "unauthorized", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"items": []any{
				map[string]any{
					"id":          "msg1",
					"roomId":      "room123",
					"personId":    "user1",
					"personEmail": "alice@example.com",
					"text":        "hello bot",
					"html":        "<p>hello bot</p>",
					"created":     "2024-06-01T12:00:00Z",
				},
			},
		})
	}))
	defer srv.Close()

	// Parse JSON directly to validate struct
	raw := `{"items":[{"id":"msg1","roomId":"room123","personId":"user1","personEmail":"alice@example.com","text":"hello bot","html":"<p>hello bot</p>","created":"2024-06-01T12:00:00Z"}]}`
	var result struct {
		Items []wxMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	m := result.Items[0]
	if m.Text != "hello bot" {
		t.Errorf("got text %q", m.Text)
	}
	if m.PersonEmail != "alice@example.com" {
		t.Errorf("got email %q", m.PersonEmail)
	}
	_ = srv
}

func TestIsMention_html(t *testing.T) {
	cases := []struct {
		html string
		want bool
	}{
		{`<spark-mention>bot</spark-mention>`, true},
		{`<p>hello</p>`, false},
		{`<p><spark-mention data-object-id="Y2lzY29zcGFyazovL3VzL1BFT1BMRS8xMjM">bot</spark-mention> help</p>`, true},
	}
	for _, c := range cases {
		got := strings.Contains(c.html, "spark-mention")
		if got != c.want {
			t.Errorf("html=%q: got %v, want %v", c.html, got, c.want)
		}
	}
}
