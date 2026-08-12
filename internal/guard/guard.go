// Package guard protects the front door: how many connections one address may
// hold open, and how fast it may try to hand shake.
//
// Everything past the handshake is already rate limited per user — chat, paint,
// uploads. But those limits only exist once someone is a user. A client that
// never finishes a handshake has no identity to limit, so the limit here is by
// address, and it is what stops one machine from opening ten thousand sockets.
package guard

import (
	"errors"
	"net"
	"strings"
	"sync"

	"tamizchat/internal/ratelimit"
)

// Errors the gateway maps onto HTTP statuses and close reasons.
var (
	// ErrTooManyConnections means this address already holds the maximum
	// number of open sockets.
	ErrTooManyConnections = errors.New("too many connections from this address")
	// ErrTooFast means this address is opening connections faster than allowed.
	ErrTooFast = errors.New("too many connection attempts")
)

// Limits are read on every attempt, so an admin can change them without a
// restart.
type Limits struct {
	// MaxPerIP is how many sockets one address may hold at once. Zero disables
	// the limit, which is what a server behind a single NAT may want.
	MaxPerIP int
	// Burst and PerMinute bound how fast new connections may be opened.
	Burst     int
	PerMinute int
}

// Guard tracks connections per address.
type Guard struct {
	limits func() Limits

	mu      sync.Mutex
	open    map[string]int
	buckets map[string]*ratelimit.Bucket
}

// New builds a guard.
func New(limits func() Limits) *Guard {
	return &Guard{
		limits:  limits,
		open:    make(map[string]int),
		buckets: make(map[string]*ratelimit.Bucket),
	}
}

// Admit accounts for a new connection from addr. The returned function must be
// called when the connection ends — deferring it at the point of admission is
// the only way to be sure a socket is not counted forever.
func (g *Guard) Admit(addr string) (release func(), err error) {
	ip := Host(addr)

	limits := g.limits()

	g.mu.Lock()
	defer g.mu.Unlock()

	if limits.MaxPerIP > 0 && g.open[ip] >= limits.MaxPerIP {
		return nil, ErrTooManyConnections
	}

	bucket, ok := g.buckets[ip]
	if !ok {
		bucket = &ratelimit.Bucket{}
		g.buckets[ip] = bucket
	}
	if limits.PerMinute > 0 && !bucket.Allow(limits.Burst, float64(limits.PerMinute)/60) {
		return nil, ErrTooFast
	}

	g.open[ip]++
	return func() { g.done(ip) }, nil
}

func (g *Guard) done(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.open[ip] <= 1 {
		// Drop the counter entirely rather than leaving a zero behind: a busy
		// server sees many addresses, and each one that leaves should leave
		// nothing behind. The rate bucket stays until the sweep, because
		// forgetting it immediately would reset the rate limit on every
		// disconnect.
		delete(g.open, ip)
		return
	}
	g.open[ip]--
}

// Open reports how many connections an address currently holds.
func (g *Guard) Open(addr string) int {
	ip := Host(addr)
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open[ip]
}

// Sweep drops rate buckets for addresses with nothing open. Without it a
// long-running server slowly accumulates one bucket per address it has ever
// seen.
func (g *Guard) Sweep() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	removed := 0
	for ip := range g.buckets {
		if g.open[ip] == 0 {
			delete(g.buckets, ip)
			removed++
		}
	}
	return removed
}

// Host extracts the address part of a "host:port" pair, leaving anything
// unusual untouched so it can still be counted as one bucket.
func Host(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.TrimSpace(addr)
}
