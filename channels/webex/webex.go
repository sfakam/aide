// Package webex implements a Webex REST polling channel adapter.
package webex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sfathall/aide/channels"
)

const apiBase = "https://webexapis.com/v1"

// Channel polls the Webex REST API for new messages in configured rooms.
type Channel struct {
	id           string
	token        string
	roomID       string // the Webex room to watch (may be resolved at Start time)
	direct       bool   // true for 1:1 direct rooms — skips mentionedPeople filter
	directWith   string // if set, resolve roomID from this person's email at Start time
	botID        string // bot's own person ID (to detect mentions and skip self-messages)
	pollInterval time.Duration
	log          *slog.Logger

	mu       sync.Mutex // protects lastSeen (written by Start goroutine, read by SaveCursor)
	lastSeen time.Time  // ISO timestamp of last processed message

	typingSupported atomic.Bool // flipped to false on first 404 from typing endpoint
}

// New creates a Webex Channel adapter.
// If directWith is non-empty, roomID is auto-discovered at Start time via the
// Webex API by finding or creating a 1:1 room with that person's email.
func New(id, token, roomID, directWith string, direct bool, pollInterval time.Duration) *Channel {
	c := &Channel{
		id:           id,
		token:        token,
		roomID:       roomID,
		direct:       direct || directWith != "",
		directWith:   directWith,
		pollInterval: pollInterval,
		log:          slog.With("channel", id, "type", "webex"),
		lastSeen:     time.Now().UTC(),
	}
	c.typingSupported.Store(true)
	return c
}

func (c *Channel) ID() string { return c.id }

// Start begins polling. Blocks until ctx is cancelled.
func (c *Channel) Start(ctx context.Context, inbound chan<- channels.InboundMessage) error {
	if c.botID == "" {
		id, err := c.fetchBotIDWithRetry(ctx)
		if err != nil {
			return fmt.Errorf("could not fetch bot ID (self-echo filter disabled — refusing to poll): %w", err)
		}
		c.botID = id
	}

	if c.roomID == "" && c.directWith != "" {
		roomID, err := c.resolveDirectRoom(ctx, c.directWith)
		if err != nil {
			return fmt.Errorf("resolve direct room with %q: %w", c.directWith, err)
		}
		c.roomID = roomID
		c.log.Info("direct room resolved", "person", c.directWith, "room_id", roomID)
	}

	c.log.Info("starting", "room", c.roomID, "interval", c.pollInterval, "since", c.lastSeen.UTC().Format(time.RFC3339Nano))
	cycle := 0
	for {
		cycle++
		c.log.Debug("poll cycle", "cycle", cycle, "last_seen", c.lastSeen.Format(time.RFC3339))

		msgs, err := c.fetchMessages(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.log.Warn("fetchMessages error", "cycle", cycle, "err", err)
		} else {
			c.log.Debug("poll result", "cycle", cycle, "fetched", len(msgs))
			dispatched := 0
			for _, m := range msgs {
				if m.PersonID == c.botID {
					c.log.Debug("skipping own message", "msg_id", m.ID)
					continue
				}
				created := parseWebexTime(m.Created)
				if !created.After(c.lastSeen) {
					c.log.Debug("skipping seen message", "msg_id", m.ID, "created", m.Created)
					continue
				}
				// Advance cursor BEFORE sending to inbound so SaveCursor() is
				// never called with a stale value regardless of channel buffering.
				c.mu.Lock()
				c.lastSeen = created
				c.mu.Unlock()
				c.log.Debug("cursor advanced", "last_seen", created.UTC().Format(time.RFC3339Nano))
				isMention := strings.Contains(m.HTML, "spark-mention")
				c.log.Debug("dispatching message",
					"msg_id", m.ID,
					"from", m.PersonEmail,
					"mention", isMention,
					"text", truncate(m.Text, 80),
				)
				inbound <- channels.InboundMessage{
					ChannelID:  c.id,
					RoomID:     m.RoomID,
					SenderID:   m.PersonID,
					SenderName: m.PersonEmail,
					Text:       m.Text,
					IsMention:  isMention,
				}
				dispatched++
			}
			if dispatched > 0 {
				c.log.Info("dispatched messages", "cycle", cycle, "count", dispatched)
			}
		}

		c.log.Debug("poll sleeping", "cycle", cycle, "duration", c.pollInterval)
		select {
		case <-time.After(c.pollInterval):
		case <-ctx.Done():
			return nil
		}
	}
}

// webexMaxLen is the conservative character limit for a single Webex message
// (the hard API limit is 7439 bytes before encryption).
const webexMaxLen = 7200

// Send posts a message to the configured room, splitting it into multiple
// messages if it exceeds webexMaxLen to preserve formatting.
func (c *Channel) Send(ctx context.Context, roomID, text string) error {
	for _, chunk := range splitMessage(text) {
		if err := c.sendOne(ctx, roomID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (c *Channel) sendOne(ctx context.Context, roomID, text string) error {
	payload := fmt.Sprintf(`{"roomId":%s,"markdown":%s}`, jsonString(roomID), jsonString(text))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+"/messages", strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webex sendMessage HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

// splitMessage splits text into chunks of at most webexMaxLen bytes, breaking
// only at paragraph boundaries (double newline), then line boundaries, never
// inside a fenced code block.
func splitMessage(text string) []string {
	if len(text) <= webexMaxLen {
		return []string{text}
	}
	split := findSplit(text, webexMaxLen)
	first := strings.TrimRight(text[:split], "\n")
	rest := strings.TrimLeft(text[split:], "\n")
	if rest == "" {
		return []string{first}
	}
	return append([]string{first}, splitMessage(rest)...)
}

// findSplit returns the index at which text should be split so that text[:i]
// is at most maxLen bytes and does not end inside a fenced code block.
// It prefers double-newline (paragraph) breaks, then single-newline breaks,
// then a hard byte cut as a last resort.
func findSplit(text string, maxLen int) int {
	if maxLen > len(text) {
		maxLen = len(text)
	}
	// Prefer paragraph boundary (\n\n).
	for i := maxLen - 1; i > 1; i-- {
		if text[i] == '\n' && text[i-1] == '\n' && !inCodeFence(text[:i]) {
			return i + 1
		}
	}
	// Fall back to line boundary (\n).
	for i := maxLen - 1; i > 0; i-- {
		if text[i] == '\n' && !inCodeFence(text[:i]) {
			return i + 1
		}
	}
	return maxLen
}

// inCodeFence reports whether text ends inside an unclosed fenced code block.
// It counts lines that begin with ``` or ~~~; an odd count means still inside.
func inCodeFence(text string) bool {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			count++
		}
	}
	return count%2 == 1
}

// SendTyping sends a typing-indicator pulse to roomID.
// The indicator is visible for ~10 seconds; call it every ~8s to sustain it.
// Implements channels.TypingChannel.
// If the endpoint returns 404 (not supported for this bot/org), the feature is
// silently disabled for the lifetime of this Channel instance.
func (c *Channel) SendTyping(ctx context.Context, roomID string) error {
	if !c.typingSupported.Load() {
		return nil
	}
	url := fmt.Sprintf("%s/rooms/%s/typing", apiBase, roomID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		c.typingSupported.Store(false)
		c.log.Info("typing indicator not supported by this Webex org — disabled")
		return nil
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webex typing HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

// LoadCursor restores lastSeen from a saved ISO timestamp string.
func (c *Channel) LoadCursor(cursor string) {
	if cursor == "" {
		return
	}
	if t := parseWebexTime(cursor); !t.IsZero() {
		c.mu.Lock()
		c.lastSeen = t
		c.mu.Unlock()
	}
}

// SaveCursor returns lastSeen as an ISO timestamp string with nanosecond
// precision so sub-second Webex message timestamps survive a restart.
func (c *Channel) SaveCursor() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSeen.UTC().Format(time.RFC3339Nano)
}

// parseWebexTime parses a Webex ISO timestamp, accepting both RFC3339Nano
// (with sub-seconds) and plain RFC3339. Returns zero time on failure.
func parseWebexTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// ─── API types ────────────────────────────────────────────────────────────────

type wxMessage struct {
	ID          string `json:"id"`
	RoomID      string `json:"roomId"`
	PersonID    string `json:"personId"`
	PersonEmail string `json:"personEmail"`
	Text        string `json:"text"`
	HTML        string `json:"html"`
	Created     string `json:"created"`
}

// ─── Internal ─────────────────────────────────────────────────────────────────

func (c *Channel) fetchMessages(ctx context.Context) ([]wxMessage, error) {
	// Direct (1:1) rooms deliver all messages to the bot without a mention filter.
	// Group spaces require mentionedPeople=me — bots can't read unmentioned messages.
	url := fmt.Sprintf("%s/messages?roomId=%s&max=50", apiBase, c.roomID)
	if !c.direct {
		url += "&mentionedPeople=me"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		wait := retryAfter(resp, 60*time.Second)
		c.log.Warn("rate limited — backing off", "wait", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
		return nil, fmt.Errorf("webex listMessages HTTP 429 (backed off %s)", wait)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("webex listMessages HTTP %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Items []wxMessage `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Items, nil
}

// retryAfter reads the Retry-After header value (seconds) and returns the
// corresponding duration, falling back to defaultWait if the header is absent
// or unparseable.
func retryAfter(resp *http.Response, defaultWait time.Duration) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultWait
}

// resolveDirectRoom finds the 1:1 room between this bot and personEmail.
// It lists direct rooms for the bot and checks memberships. If no room exists
// yet, it creates one by sending an initial message (which Webex auto-creates
// the room for), then returns the new room ID.
func (c *Channel) resolveDirectRoom(ctx context.Context, personEmail string) (string, error) {
	// Walk paginated direct rooms looking for personEmail as a member.
	url := fmt.Sprintf("%s/rooms?type=direct&max=50", apiBase)
	for url != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		var page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		for _, room := range page.Items {
			ok, err := c.roomHasMember(ctx, room.ID, personEmail)
			if err != nil {
				c.log.Debug("membership check failed", "room", room.ID, "err", err)
				continue
			}
			if ok {
				return room.ID, nil
			}
		}
		// Webex uses Link header for pagination; stop if no next page.
		link := resp.Header.Get("Link")
		url = nextPageURL(link)
	}

	// No existing room — create one by sending an initial message.
	payload := fmt.Sprintf(`{"toPersonEmail":%s,"text":"👋 aide bot connected."}`, jsonString(personEmail))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		apiBase+"/messages", strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create direct room HTTP %d: %s", resp.StatusCode, body)
	}
	var msg struct {
		RoomID string `json:"roomId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return "", err
	}
	return msg.RoomID, nil
}

// roomHasMember returns true if personEmail is a member of roomID.
func (c *Channel) roomHasMember(ctx context.Context, roomID, personEmail string) (bool, error) {
	url := fmt.Sprintf("%s/memberships?roomId=%s&personEmail=%s", apiBase, roomID, personEmail)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var result struct {
		Items []struct{ ID string } `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return len(result.Items) > 0, nil
}

// nextPageURL parses a Webex Link header and returns the "next" URL, or "".
func nextPageURL(link string) string {
	// Format: <https://...>; rel="next"
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, `rel="next"`) {
			if start := strings.Index(part, "<"); start >= 0 {
				if end := strings.Index(part, ">"); end > start {
					return part[start+1 : end]
				}
			}
		}
	}
	return ""
}

// fetchBotIDWithRetry calls fetchBotID with exponential backoff (1s→2s→4s…→60s)
// until it succeeds or ctx is cancelled. The bot ID is required for the
// self-echo filter; polling must not start without it.
func (c *Channel) fetchBotIDWithRetry(ctx context.Context) (string, error) {
	delays := []time.Duration{time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}
	i := 0
	for {
		id, err := c.fetchBotID(ctx)
		if err == nil {
			return id, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		delay := delays[min(i, len(delays)-1)]
		i++
		c.log.Warn("fetchBotID failed — retrying", "err", err, "backoff", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (c *Channel) fetchBotID(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/people/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var me struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return "", err
	}
	return me.ID, nil
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
