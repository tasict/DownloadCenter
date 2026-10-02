package dl

import (
	"context"
	"sync"
	"time"
)

// limiter is a token bucket for one task's download speed; the rate can
// change while the download runs.
type limiter struct {
	mu     sync.Mutex
	rate   int64 // bytes/s, 0 = unlimited
	tokens float64
	last   time.Time
}

func (l *limiter) set(rate int64) {
	l.mu.Lock()
	if rate < 0 {
		rate = 0
	}
	l.rate = rate
	l.mu.Unlock()
}

// chunk is how much to read at once: small reads keep slow limits smooth.
func (l *limiter) chunk(limit int) int {
	l.mu.Lock()
	r := l.rate
	l.mu.Unlock()
	if r <= 0 {
		return limit
	}
	return int(min(max(r/8, 1024), int64(limit)))
}

// wait blocks until n bytes fit in the rate.
func (l *limiter) wait(ctx context.Context, n int) error {
	for {
		l.mu.Lock()
		if l.rate <= 0 {
			l.last = time.Time{}
			l.mu.Unlock()
			return nil
		}
		now := time.Now()
		if l.last.IsZero() {
			l.last = now
		}
		rate := float64(l.rate)
		l.tokens += now.Sub(l.last).Seconds() * rate
		l.last = now
		burst := max(rate/4, float64(n))
		if l.tokens > burst {
			l.tokens = burst
		}
		if l.tokens >= float64(n) {
			l.tokens -= float64(n)
			l.mu.Unlock()
			return nil
		}
		d := time.Duration((float64(n) - l.tokens) / rate * float64(time.Second))
		l.mu.Unlock()
		d = min(max(d, time.Millisecond), 200*time.Millisecond)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
}
