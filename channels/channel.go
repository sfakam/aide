// Package channels defines the shared types and interface for all channel adapters.
package channels

import "context"

// InboundMessage is a normalised message received from any channel.
type InboundMessage struct {
	ChannelID  string // matches Channel.ID in config
	RoomID     string // platform-specific room/chat identifier
	SenderID   string // platform-specific sender identifier
	SenderName string // human-readable sender name
	Text       string
	IsMention  bool // true when the bot was @mentioned in a group room
}

// Sender is a callback supplied by the router to deliver a reply back to the channel.
// roomID is the same value from InboundMessage.RoomID.
type Sender func(ctx context.Context, roomID, text string) error

// TypingChannel is an optional interface an adapter may implement to show a
// "typing" indicator while the bot is processing a message. The router checks
// for this interface via type assertion; adapters that don't support it are
// silently skipped.
type TypingChannel interface {
	// SendTyping sends a single typing-indicator pulse to roomID.
	// The indicator typically lasts ~10 seconds; call periodically to sustain it.
	SendTyping(ctx context.Context, roomID string) error
}

// Channel is the interface every adapter must implement.
type Channel interface {
	// ID returns the config id of this channel (e.g. "my-telegram").
	ID() string

	// Start begins polling. inbound receives every new message.
	// The channel must stop when ctx is cancelled.
	Start(ctx context.Context, inbound chan<- InboundMessage) error

	// Send delivers a reply to roomID.
	Send(ctx context.Context, roomID, text string) error

	// LoadCursor restores the deduplication cursor from a previously saved string.
	// Called once before Start.
	LoadCursor(cursor string)

	// SaveCursor returns the current cursor to be persisted.
	// Called periodically by the main loop after each poll tick.
	SaveCursor() string
}
