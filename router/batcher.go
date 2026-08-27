package router

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// statusBatcher accumulates tool-call labels over a time window and sends a
// single grouped summary message per interval instead of one message per call.
type statusBatcher struct {
	mu       sync.Mutex
	counts   map[string]int // tool key → call count
	order    []string       // insertion-order keys for stable display
	sendFn   func(string)
	start    time.Time
	interval time.Duration
	timer    *time.Timer
	done     bool
}

// newStatusBatcher creates a batcher that calls sendFn with a summary every
// interval. Call flush() when the response is complete to drain any remainder.
func newStatusBatcher(sendFn func(string), interval time.Duration) *statusBatcher {
	b := &statusBatcher{
		counts:   make(map[string]int),
		sendFn:   sendFn,
		start:    time.Now(),
		interval: interval,
	}
	b.timer = time.AfterFunc(interval, b.tick)
	return b
}

// add records one tool-call label.
func (b *statusBatcher) add(label string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	key := batchKey(label)
	if _, ok := b.counts[key]; !ok {
		b.order = append(b.order, key)
	}
	b.counts[key]++
}

// flush stops the timer and sends any pending content immediately.
// Safe to call multiple times (idempotent after the first call).
func (b *statusBatcher) flush() {
	b.timer.Stop()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	b.done = true
	b.emit()
}

// tick fires on the periodic interval.
func (b *statusBatcher) tick() {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.emit()
	// Re-arm only if not yet flushed.
	b.timer = time.AfterFunc(b.interval, b.tick)
	b.mu.Unlock()
}

// emit sends the current batch summary and resets counts. Must be called with mu held.
func (b *statusBatcher) emit() {
	if len(b.counts) == 0 {
		return
	}
	elapsed := time.Since(b.start).Round(time.Second)
	parts := make([]string, 0, len(b.order))
	for _, key := range b.order {
		n := b.counts[key]
		if n == 1 {
			parts = append(parts, key)
		} else {
			parts = append(parts, fmt.Sprintf("%s ×%d", key, n))
		}
	}
	msg := fmt.Sprintf("⚙️ Working (%s)… %s", elapsed, strings.Join(parts, " · "))
	b.sendFn(msg)
	// Reset for next window.
	b.counts = make(map[string]int)
	b.order = nil
}

// batchKey extracts a short "emoji ToolName" key from a full label like
// "🖥️ Bash: some long command" or "🔧 mcp__confluence__list_child_pages…".
func batchKey(label string) string {
	// Labels are: "<emoji> <Name>: <detail>" or "<emoji> <Name>…"
	// Strip everything from the first ":" or "…" onward.
	for i, r := range label {
		if r == ':' || r == '…' {
			return strings.TrimSpace(label[:i])
		}
	}
	return strings.TrimSpace(label)
}
