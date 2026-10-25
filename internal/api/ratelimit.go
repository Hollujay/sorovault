package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimitOptions configures the per-client limit on registrations and
// refreshes. Zero values disable the limit.
type RateLimitOptions struct {
	// PerMinute is how many requests one client may make per minute.
	PerMinute int
	// Burst is how many may arrive at once before the sustained rate
	// applies.
	Burst int
	// TrustXForwardedFor makes the first X-Forwarded-For address the client
	// key. It must only be enabled behind a proxy the operator controls:
	// otherwise a caller can pick a new address on every request and never
	// hit the limit.
	TrustXForwardedFor bool
}

// SetRateLimit enables per-client limiting on the two expensive write
// routes. It must be called before Routes.
func (s *Server) SetRateLimit(opts RateLimitOptions) {
	if opts.PerMinute <= 0 {
		s.rateLimit = nil
		return
	}
	burst := opts.Burst
	if burst < 1 {
		burst = 1
	}
	s.rateLimit = newClientLimiter(rate.Limit(float64(opts.PerMinute)/60), burst, opts.TrustXForwardedFor)
}

// rateLimited wraps a handler with the client limit, when one is configured.
func (s *Server) rateLimited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.rateLimit == nil {
			next(w, r)
			return
		}

		wait, ok := s.rateLimit.allow(r)
		if ok {
			next(w, r)
			return
		}

		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(wait)))
		writeError(w, http.StatusTooManyRequests,
			"rate limit exceeded; try again in "+strconv.Itoa(retryAfterSeconds(wait))+"s")
	}
}

// clientLimiter keeps one token bucket per client.
//
// Buckets are evicted after a period of inactivity, so a stream of distinct
// addresses cannot grow the map without bound.
type clientLimiter struct {
	mu        sync.Mutex
	limit     rate.Limit
	burst     int
	trust     bool
	clients   map[string]*clientEntry
	lastSweep time.Time
	now       func() time.Time
}

type clientEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

const (
	// clientIdleTTL is how long an idle client's bucket is kept. It is much
	// longer than the time needed to refill a bucket, so eviction never
	// resets a limit that is still in force.
	clientIdleTTL = 10 * time.Minute
	// clientSweepInterval bounds how often the map is scanned.
	clientSweepInterval = time.Minute
)

func newClientLimiter(limit rate.Limit, burst int, trust bool) *clientLimiter {
	return &clientLimiter{
		limit:   limit,
		burst:   burst,
		trust:   trust,
		clients: make(map[string]*clientEntry),
		now:     time.Now,
	}
}

// allow consumes a token for the request's client, or returns how long the
// client should wait before retrying.
func (l *clientLimiter) allow(r *http.Request) (time.Duration, bool) {
	key := l.clientKey(r)

	l.mu.Lock()
	now := l.now()
	l.evictLocked(now)
	entry, ok := l.clients[key]
	if !ok {
		entry = &clientEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.clients[key] = entry
	}
	entry.lastSeen = now
	limiter := entry.limiter
	l.mu.Unlock()

	reservation := limiter.Reserve()
	if delay := reservation.Delay(); delay > 0 {
		reservation.Cancel()
		return delay, false
	}
	return 0, true
}

// evictLocked drops buckets that have been idle long enough. The caller
// holds l.mu.
func (l *clientLimiter) evictLocked(now time.Time) {
	if len(l.clients) == 0 {
		return
	}
	if l.lastSweep.IsZero() {
		l.lastSweep = now
		return
	}
	if now.Sub(l.lastSweep) < clientSweepInterval {
		return
	}
	l.lastSweep = now
	for key, entry := range l.clients {
		if now.Sub(entry.lastSeen) >= clientIdleTTL {
			delete(l.clients, key)
		}
	}
}

// clientKey identifies the caller. X-Forwarded-For is only consulted when
// the operator has said a trusted proxy sets it.
func (l *clientLimiter) clientKey(r *http.Request) string {
	if l.trust {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			if first := strings.TrimSpace(strings.Split(forwarded, ",")[0]); first != "" {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
