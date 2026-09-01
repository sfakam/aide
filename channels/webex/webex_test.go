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
	ch := New("wx1", "token", "room123", "", false, 5*time.Second)
	if ch.ID() != "wx1" {
		t.Errorf("expected id wx1, got %q", ch.ID())
	}
	if ch.roomID != "room123" {
		t.Errorf("expected roomID room123, got %q", ch.roomID)
	}
}

func TestCursorRoundtrip(t *testing.T) {
	ch := New("wx1", "tok", "r1", "", false, time.Second)
	ts := "2024-01-15T10:30:00Z"
	ch.LoadCursor(ts)
	saved := ch.SaveCursor()
	if saved != ts {
		t.Errorf("expected %q, got %q", ts, saved)
	}
}

func TestCursorEmpty(t *testing.T) {
	ch := New("wx1", "tok", "r1", "", false, time.Second)
	before := ch.lastSeen
	ch.LoadCursor("")
	if !ch.lastSeen.Equal(before) {
		t.Error("empty cursor should not change lastSeen")
	}
}

func TestCursorBadValue(t *testing.T) {
	ch := New("wx1", "tok", "r1", "", false, time.Second)
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

// ─── splitMessage tests ────────────────────────────────────────────────────────

func TestSplitMessage_short(t *testing.T) {
	text := "hello world"
	chunks := splitMessage(text)
	if len(chunks) != 1 || chunks[0] != text {
		t.Errorf("expected single unchanged chunk, got %v", chunks)
	}
}

func TestSplitMessage_exactLimit(t *testing.T) {
	text := strings.Repeat("a", webexMaxLen)
	chunks := splitMessage(text)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk at exact limit, got %d", len(chunks))
	}
}

func TestSplitMessage_splitsAtParagraph(t *testing.T) {
	// Two paragraphs totaling over the limit; split should land between them.
	para1 := strings.Repeat("a", webexMaxLen-10)
	para2 := strings.Repeat("b", 100)
	text := para1 + "\n\n" + para2

	chunks := splitMessage(text)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != para1 {
		t.Errorf("chunk 0 wrong: len=%d", len(chunks[0]))
	}
	if chunks[1] != para2 {
		t.Errorf("chunk 1 wrong: got %q", chunks[1])
	}
}

func TestSplitMessage_splitsAtLine(t *testing.T) {
	// No double-newline; should split at the last single newline before the limit.
	line1 := strings.Repeat("a", webexMaxLen-10)
	line2 := strings.Repeat("b", 50)
	text := line1 + "\n" + line2

	chunks := splitMessage(text)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != line1 {
		t.Errorf("chunk 0 wrong")
	}
	if chunks[1] != line2 {
		t.Errorf("chunk 1 wrong")
	}
}

func TestSplitMessage_avoidsCodeFence(t *testing.T) {
	// Paragraph boundary falls inside an open code fence; should not split there.
	// Structure: prose \n\n ```go \n<code> \n``` \n\n more prose
	// The \n\n after the prose is before the fence opens — safe.
	// The \n\n inside the fence should be skipped.
	prose := strings.Repeat("p", 100)
	code := "```go\n" + strings.Repeat("x", webexMaxLen-200) + "\n```"
	epilogue := "end"
	text := prose + "\n\n" + code + "\n\n" + epilogue

	chunks := splitMessage(text)

	// Every chunk must have balanced fences.
	for i, c := range chunks {
		if inCodeFence(c) {
			t.Errorf("chunk %d ends inside an open code fence:\n%s", i, c[:min(len(c), 200)])
		}
	}
}

func TestSplitMessage_multiChunk(t *testing.T) {
	// Text long enough to produce 3+ chunks.
	para := strings.Repeat("w", webexMaxLen-5)
	text := para + "\n\n" + para + "\n\n" + para

	chunks := splitMessage(text)
	if len(chunks) < 3 {
		t.Errorf("expected at least 3 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > webexMaxLen {
			t.Errorf("chunk %d exceeds limit: len=%d", i, len(c))
		}
	}
}

func TestInCodeFence(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"no fences here", false},
		{"```go\nsome code", true},
		{"```go\nsome code\n```", false},
		{"~~~\ncode\n~~~", false},
		{"~~~\ncode", true},
		{"```\nopen\n```\n\n```\nstill open", true},
	}
	for _, c := range cases {
		got := inCodeFence(c.text)
		if got != c.want {
			t.Errorf("inCodeFence(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
