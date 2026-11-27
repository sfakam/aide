package agent

import "testing"

func TestRateLimiter_allowsUnderLimit(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 5; i++ {
		if !r.Allow("a1", 5, 60) {
			t.Fatalf("request %d unexpectedly denied", i)
		}
	}
}

func TestRateLimiter_deniesOverLimit(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 3; i++ {
		r.Allow("a1", 3, 60)
	}
	if r.Allow("a1", 3, 60) {
		t.Error("expected 4th request to be denied")
	}
}

func TestRateLimiter_independentPerAgent(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 3; i++ {
		r.Allow("a1", 3, 60)
	}
	if !r.Allow("a2", 3, 60) {
		t.Error("a2 should have its own counter")
	}
}

func TestRateLimiter_zeroLimitMeansUnlimited(t *testing.T) {
	r := newRateLimiter()
	for i := 0; i < 100; i++ {
		if !r.Allow("a1", 0, 60) {
			t.Fatalf("request %d unexpectedly denied with limit=0", i)
		}
	}
}
