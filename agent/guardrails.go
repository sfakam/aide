package agent

import (
	"sync"
	"time"
)

// rateLimiter enforces a fixed-window request cap per agent. Counters are
// in-memory only and reset on process restart — see docs/design.md §7 in the
// aide-agents repo for why that's an accepted trade-off.
type rateLimiter struct {
	mu       sync.Mutex
	counters map[string]*window
}

type window struct {
	start time.Time
	count int
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{counters: make(map[string]*window)}
}

// Allow reports whether agentID may make another request under limit
// requests per windowMinutes, recording the request if so.
func (r *rateLimiter) Allow(agentID string, limit, windowMinutes int) bool {
	if limit <= 0 {
		return true
	}
	now := time.Now()
	windowDur := time.Duration(windowMinutes) * time.Minute

	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.counters[agentID]
	if !ok || now.Sub(w.start) >= windowDur {
		r.counters[agentID] = &window{start: now, count: 1}
		return true
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}
