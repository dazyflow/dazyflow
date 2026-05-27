package daemon

import "testing"

func TestIPRateLimiter_BurstThenBlock(t *testing.T) {
	// 60/min = 1 token/sec, burst 3. First 3 immediate calls pass
	// (burst), the 4th is blocked (no time to refill).
	l := newIPRateLimiter(60, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("call %d should pass (within burst)", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Error("4th call should be blocked (burst exhausted)")
	}
}

func TestIPRateLimiter_PerIPIsolation(t *testing.T) {
	l := newIPRateLimiter(60, 1)
	if !l.Allow("a") {
		t.Fatal("first IP-a call should pass")
	}
	if l.Allow("a") {
		t.Fatal("second IP-a call should block")
	}
	// Different IP has its own bucket — unaffected by IP-a's exhaustion.
	if !l.Allow("b") {
		t.Error("IP-b should have its own full bucket")
	}
}

func TestNewAuthRateLimiter_DisabledWhenZero(t *testing.T) {
	if NewAuthRateLimiter(0, 5) != nil {
		t.Error("perMinute=0 should disable (nil) the limiter")
	}
}
