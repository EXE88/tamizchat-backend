package guard

import (
	"errors"
	"testing"
)

func fixed(limits Limits) func() Limits { return func() Limits { return limits } }

func TestPerAddressConnectionCap(t *testing.T) {
	g := New(fixed(Limits{MaxPerIP: 2, Burst: 100, PerMinute: 6000}))

	first, err := g.Admit("10.0.0.1:1000")
	if err != nil {
		t.Fatalf("first connection: %v", err)
	}
	if _, err := g.Admit("10.0.0.1:1001"); err != nil {
		t.Fatalf("second connection: %v", err)
	}

	if _, err := g.Admit("10.0.0.1:1002"); !errors.Is(err, ErrTooManyConnections) {
		t.Fatalf("the third should be refused, got %v", err)
	}

	// A different address is unaffected: the limit is per client, not global.
	if _, err := g.Admit("10.0.0.2:1000"); err != nil {
		t.Fatalf("another address should be admitted: %v", err)
	}

	// Releasing frees a slot.
	first()
	if _, err := g.Admit("10.0.0.1:1003"); err != nil {
		t.Fatalf("a released slot should be reusable: %v", err)
	}
}

func TestReleaseIsAccountedExactly(t *testing.T) {
	g := New(fixed(Limits{MaxPerIP: 3, Burst: 100, PerMinute: 6000}))

	var releases []func()
	for i := 0; i < 3; i++ {
		release, err := g.Admit("10.0.0.1:1000")
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	if got := g.Open("10.0.0.1:1000"); got != 3 {
		t.Fatalf("expected 3 open, got %d", got)
	}

	for _, release := range releases {
		release()
	}
	if got := g.Open("10.0.0.1:1000"); got != 0 {
		t.Fatalf("everything was released, got %d open", got)
	}
}

func TestConnectionRateIsLimited(t *testing.T) {
	// Three attempts allowed back to back, then effectively nothing for a
	// while: one per minute refills far too slowly to matter in a test.
	g := New(fixed(Limits{MaxPerIP: 0, Burst: 3, PerMinute: 1}))

	for i := 0; i < 3; i++ {
		if _, err := g.Admit("10.0.0.1:1000"); err != nil {
			t.Fatalf("attempt %d should be allowed: %v", i+1, err)
		}
	}
	if _, err := g.Admit("10.0.0.1:1000"); !errors.Is(err, ErrTooFast) {
		t.Fatalf("the burst is spent, got %v", err)
	}
}

// A server where everyone shares one NAT address needs the cap off.
func TestZeroMeansUnlimitedConnections(t *testing.T) {
	g := New(fixed(Limits{MaxPerIP: 0, Burst: 1000, PerMinute: 60000}))

	for i := 0; i < 50; i++ {
		if _, err := g.Admit("10.0.0.1:1000"); err != nil {
			t.Fatalf("connection %d should be allowed: %v", i, err)
		}
	}
}

// A rate bucket per address would otherwise be kept for every address the
// server has ever seen.
func TestSweepDropsIdleAddresses(t *testing.T) {
	g := New(fixed(Limits{MaxPerIP: 5, Burst: 10, PerMinute: 600}))

	release, err := g.Admit("10.0.0.1:1000")
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if _, err := g.Admit("10.0.0.2:1000"); err != nil {
		t.Fatalf("admit: %v", err)
	}

	// 10.0.0.1 goes away; 10.0.0.2 is still connected.
	release()
	if removed := g.Sweep(); removed != 1 {
		t.Fatalf("only the idle address should be swept, removed %d", removed)
	}
	if g.Open("10.0.0.2:1000") != 1 {
		t.Fatal("the connected address must survive the sweep")
	}
}

func TestHostIgnoresThePort(t *testing.T) {
	cases := map[string]string{
		"10.0.0.1:1000": "10.0.0.1",
		"[::1]:1000":    "::1",
		"10.0.0.1":      "10.0.0.1",
		" 10.0.0.1 ":    "10.0.0.1",
	}
	for in, want := range cases {
		if got := Host(in); got != want {
			t.Fatalf("Host(%q) = %q, want %q", in, got, want)
		}
	}
}
