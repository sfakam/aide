package pty

import (
	"testing"
)

func TestSanitiseKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"worker1", "worker1"},
		{"worker:channel:user", "worker_channel_user"},
		{"abc-def_ghi", "abc-def_ghi"},
		{"a b/c", "a_b_c"},
		{"UPPER", "UPPER"},
	}
	for _, c := range cases {
		got := sanitiseKey(c.in)
		if got != c.want {
			t.Errorf("sanitiseKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewManager_defaults(t *testing.T) {
	m := NewManager("claude", "/tmp/sessions", 0)
	if m.timeoutMinutes != 60 {
		t.Errorf("expected default timeout 60, got %d", m.timeoutMinutes)
	}
	if m.claudePath != "claude" {
		t.Errorf("unexpected claude path: %q", m.claudePath)
	}
}

func TestManager_ActiveSessions_empty(t *testing.T) {
	m := NewManager("claude", "/tmp/sessions", 30)
	if len(m.ActiveSessions()) != 0 {
		t.Error("expected no active sessions initially")
	}
}
