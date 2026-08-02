package auth

import (
	"fmt"
	"testing"
	"time"
)

func newTestLimiter(perIP, global int) (*Limiter, *time.Time) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(perIP, global, 15*time.Minute)
	l.SetClock(func() time.Time { return now })
	return l, &now
}

func TestLimiterAllowsUpToThePerIPLimit(t *testing.T) {
	l, _ := newTestLimiter(5, 20)

	// Five failures are tolerated; the sixth attempt is what gets blocked.
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("1.2.3.4"); !ok {
			t.Fatalf("attempt %d blocked, want allowed", i+1)
		}
		l.RecordFailure("1.2.3.4")
	}

	ok, retry := l.Allow("1.2.3.4")
	if ok {
		t.Error("sixth attempt allowed, want blocked")
	}
	if retry <= 0 || retry > 15*time.Minute {
		t.Errorf("retryAfter = %v, want a positive duration no greater than the window", retry)
	}
}

func TestLimiterIsPerIP(t *testing.T) {
	l, _ := newTestLimiter(5, 20)
	for i := 0; i < 5; i++ {
		l.RecordFailure("1.2.3.4")
	}
	if ok, _ := l.Allow("5.6.7.8"); !ok {
		t.Error("a different IP was blocked by another IP's failures")
	}
}

func TestLimiterForgetsAfterTheWindow(t *testing.T) {
	l, now := newTestLimiter(5, 20)
	for i := 0; i < 5; i++ {
		l.RecordFailure("1.2.3.4")
	}
	if ok, _ := l.Allow("1.2.3.4"); ok {
		t.Fatal("expected the IP to be blocked before the window elapses")
	}

	*now = now.Add(15*time.Minute + time.Second)
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Error("still blocked after the window elapsed, want allowed")
	}
}

func TestGlobalCeilingBlocksAFreshIP(t *testing.T) {
	l, _ := newTestLimiter(5, 20)
	for i := 0; i < 20; i++ {
		l.RecordFailure(fmt.Sprintf("10.0.0.%d", i))
	}
	ok, retry := l.Allow("192.168.1.1")
	if ok {
		t.Error("a previously unseen IP was allowed past the global ceiling")
	}
	if retry <= 0 {
		t.Errorf("retryAfter = %v, want positive", retry)
	}
}

func TestResetClearsAnIP(t *testing.T) {
	l, _ := newTestLimiter(5, 20)
	for i := 0; i < 5; i++ {
		l.RecordFailure("1.2.3.4")
	}
	l.Reset("1.2.3.4")
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Error("Allow() = false after Reset(), want true")
	}
}
