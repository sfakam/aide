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
	"sync/atomic"
	"time"

	"github.com/sfathall/aide/channels"
)

const apiBase = "https://webexapis.com/v1"

// Channel polls the Webex REST API for new messages in configured rooms.
type Channel struct {
	id           string
	token        string
	roomID       string // the Webex room to watch
	direct       bool   // true for 1:1 direct rooms — skips mentionedPeople filter
	botID        string // bot's own person ID (to detect mentions and skip self-messages)
	pollInterval time.Duration
	log          *slog.Logger
	lastSeen     time.Time // ISO timestamp of last processed message

	typingSupported atomic.Bool // flipped to false on first 404 from typing endpoint
}

// New creates a Webex Channel adapter.
func New(id, token, roomID string, direct bool, pollInterval time.Duration) *Channel {
	c := &Channel{
		id:           id,
		token:        token,
		roomID:       roomID,
		direct:       direct,
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
		id, err := c.fetchBotID(ctx)
		if err != nil {
			c.log.Warn("could not fetch bot ID — mention detection disabled", "err", err)
		} else {
			c.botID = id
		}
	}

	c.log.Info("starting", "room", c.roomID, "interval", c.pollInterval, "since", c.lastSeen.Format(time.RFC3339))
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
				created, _ := time.Parse(time.RFC3339, m.Created)
				if !created.After(c.lastSeen) {
					c.log.Debug("skipping seen message", "msg_id", m.ID, "created", m.Created)
					continue
				}
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
				if created.After(c.lastSeen) {
					c.lastSeen = created
					c.log.Debug("cursor advanced", "last_seen", c.lastSeen.Format(time.RFC3339))
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

// Send posts a message to the configured room.
func (c *Channel) Send(ctx context.Context, roomID, text string) error {
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
	t, err := time.Parse(time.RFC3339, cursor)
	if err == nil {
		c.lastSeen = t
	}
}

// SaveCursor returns lastSeen as an ISO timestamp string.
func (c *Channel) SaveCursor() string {
	return c.lastSeen.UTC().Format(time.RFC3339)
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
