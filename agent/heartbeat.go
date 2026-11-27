package agent

import (
	"sync"
	"time"
)

// HeartbeatTracker records the last time each agent reported itself healthy
// via POST /api/v1/agents/{agent_id}/heartbeat. It's shared between the
// Server (which records heartbeats) and the supervisor that runs long-lived
// "agent_mode: supervised" tasks (which reads them to detect a stuck process).
type HeartbeatTracker struct {
	mu       sync.Mutex
	lastSeen map[string]time.Time
}

// NewHeartbeatTracker creates an empty tracker.
func NewHeartbeatTracker() *HeartbeatTracker {
	return &HeartbeatTracker{lastSeen: make(map[string]time.Time)}
}

// Touch records agentID as healthy as of now.
func (h *HeartbeatTracker) Touch(agentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastSeen[agentID] = time.Now()
}

// LastSeen returns the last recorded heartbeat time for agentID, or
// (zero, false) if none has ever been recorded.
func (h *HeartbeatTracker) LastSeen(agentID string) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.lastSeen[agentID]
	return t, ok
}

// Forget removes any recorded heartbeat for agentID, used when a supervised
// process is restarted so a stale heartbeat from the previous run can't mask
// the new process failing to ever send one.
func (h *HeartbeatTracker) Forget(agentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.lastSeen, agentID)
}
