package auth

import (
	"sync"
	"time"
)

const (
	DefaultPerIPLimit  = 5
	DefaultGlobalLimit = 20
	DefaultWindow      = 15 * time.Minute
)

// Limiter counts failed logins in a sliding window, per client IP and globally.
// The global ceiling is a deliberate trade: an attacker can lock the owner out
// for up to one window, and the fallback is physical access to the PC.
type Limiter struct {
	mu          sync.Mutex
	perIP       map[string][]time.Time
	global      []time.Time
	perIPLimit  int
	globalLimit int
	window      time.Duration
	now         func() time.Time
}

func NewLimiter(perIPLimit, globalLimit int, window time.Duration) *Limiter {
	return &Limiter{
		perIP:       make(map[string][]time.Time),
		perIPLimit:  perIPLimit,
		globalLimit: globalLimit,
		window:      window,
		now:         time.Now,
	}
}

// SetClock replaces the time source. Intended for tests.
func (l *Limiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}

// Allow reports whether a login attempt from ip may proceed. When it may not,
// the duration is how long until the oldest counted failure ages out.
func (l *Limiter) Allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)

	if hits := l.perIP[ip]; len(hits) >= l.perIPLimit {
		return false, l.window - now.Sub(hits[0])
	}
	if len(l.global) >= l.globalLimit {
		return false, l.window - now.Sub(l.global[0])
	}
	return true, 0
}

// RecordFailure counts one failed login against ip and the global ceiling.
func (l *Limiter) RecordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	l.perIP[ip] = append(l.perIP[ip], now)
	l.global = append(l.global, now)
}

// Reset clears an IP's history, called after a successful login.
func (l *Limiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.perIP, ip)
}

func (l *Limiter) pruneLocked(now time.Time) {
	cutoff := now.Add(-l.window)
	l.global = pruneBefore(l.global, cutoff)
	for ip, hits := range l.perIP {
		kept := pruneBefore(hits, cutoff)
		if len(kept) == 0 {
			delete(l.perIP, ip)
			continue
		}
		l.perIP[ip] = kept
	}
}

// pruneBefore drops leading entries at or before cutoff. The slices are always
// in ascending time order because entries are only ever appended.
func pruneBefore(times []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	return times[i:]
}
