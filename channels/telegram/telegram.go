// Package telegram implements a Telegram long-poll channel adapter.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sfathall/aide/channels"
)

const apiBase = "https://api.telegram.org/bot"

// Channel polls the Telegram Bot API for new messages.
type Channel struct {
	id           string
	token        string
	pollInterval time.Duration
	log          *slog.Logger
	offset       int64 // next update_id to fetch
}

// New creates a Telegram Channel adapter.
// pollInterval is the time between getUpdates long-poll requests.
func New(id, token string, pollInterval time.Duration) *Channel {
	return &Channel{
		id:           id,
		token:        token,
		pollInterval: pollInterval,
		log:          slog.With("channel", id, "type", "telegram"),
	}
}

func (c *Channel) ID() string { return c.id }

// Start begins long-polling. Blocks until ctx is cancelled.
func (c *Channel) Start(ctx context.Context, inbound chan<- channels.InboundMessage) error {
	c.log.Info("starting", "offset", c.offset)
	for {
		updates, err := c.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.log.Warn("getUpdates error", "err", err)
			select {
			case <-time.After(c.pollInterval):
			case <-ctx.Done():
				return nil
			}
			continue
		}

		for _, u := range updates {
			msg := c.toInbound(u)
			if msg == nil {
				continue
			}
			select {
			case inbound <- *msg:
			case <-ctx.Done():
				return nil
			}
			if u.UpdateID >= c.offset {
				c.offset = u.UpdateID + 1
			}
		}

		select {
		case <-time.After(c.pollInterval):
		case <-ctx.Done():
			return nil
		}
	}
}

// Send posts a text message to a Telegram chat.
// roomID is the chat_id (numeric string).
func (c *Channel) Send(ctx context.Context, roomID, text string) error {
	url := fmt.Sprintf("%s%s/sendMessage", apiBase, c.token)
	payload := fmt.Sprintf(`{"chat_id":%s,"text":%s}`, roomID, jsonString(text))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendMessage HTTP %d: %s", resp.StatusCode, body)
	}
	return nil
}

// LoadCursor restores the offset from a previously saved cursor string.
func (c *Channel) LoadCursor(cursor string) {
	if cursor == "" {
		return
	}
	v, err := strconv.ParseInt(cursor, 10, 64)
	if err == nil {
		c.offset = v
	}
}

// SaveCursor returns the current offset as a string for persistence.
func (c *Channel) SaveCursor() string {
	return strconv.FormatInt(c.offset, 10)
}

// ─── API types ────────────────────────────────────────────────────────────────

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int64    `json:"message_id"`
	From      *tgUser  `json:"from"`
	Chat      tgChat   `json:"chat"`
	Text      string   `json:"text"`
	Entities  []tgEntity `json:"entities"`
}

type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type tgEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// ─── Internal ─────────────────────────────────────────────────────────────────

func (c *Channel) getUpdates(ctx context.Context) ([]tgUpdate, error) {
	url := fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=25", apiBase, c.token, c.offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram API returned ok=false")
	}
	return result.Result, nil
}

func (c *Channel) toInbound(u tgUpdate) *channels.InboundMessage {
	if u.Message == nil || u.Message.Text == "" {
		return nil
	}
	m := u.Message
	roomID := strconv.FormatInt(m.Chat.ID, 10)
	senderID := ""
	senderName := ""
	if m.From != nil {
		senderID = strconv.FormatInt(m.From.ID, 10)
		senderName = strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
		if senderName == "" {
			senderName = m.From.Username
		}
	}
	isMention := false
	for _, e := range m.Entities {
		if e.Type == "mention" || e.Type == "bot_command" {
			isMention = true
			break
		}
	}
	return &channels.InboundMessage{
		ChannelID:  c.id,
		RoomID:     roomID,
		SenderID:   senderID,
		SenderName: senderName,
		Text:       m.Text,
		IsMention:  isMention,
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
