package ratelimit

import (
	"testing"
	"time"
)

func TestBurstThenRefill(t *testing.T) {
	var b Bucket
	now := time.Unix(0, 0)

	// A fresh bucket allows a full burst.
	for i := 0; i < 3; i++ {
		if !b.allowAt(now, 3, 1) {
			t.Fatalf("message %d of the burst should be allowed", i+1)
		}
	}
	if b.allowAt(now, 3, 1) {
		t.Fatal("the burst is spent, the next message must be rejected")
	}

	// One token per second.
	if !b.allowAt(now.Add(time.Second), 3, 1) {
		t.Fatal("a token should have refilled after a second")
	}
	if b.allowAt(now.Add(time.Second), 3, 1) {
		t.Fatal("only one token refills per second")
	}
}

func TestRefillIsCappedAtCapacity(t *testing.T) {
	var b Bucket
	now := time.Unix(0, 0)
	b.allowAt(now, 2, 1)

	// An hour of silence must not buy an unlimited burst.
	later := now.Add(time.Hour)
	for i := 0; i < 2; i++ {
		if !b.allowAt(later, 2, 1) {
			t.Fatalf("message %d should be allowed", i+1)
		}
	}
	if b.allowAt(later, 2, 1) {
		t.Fatal("tokens should be capped at the burst size")
	}
}

func TestCapacityIsNeverZero(t *testing.T) {
	var b Bucket
	if !b.allowAt(time.Unix(0, 0), 0, 1) {
		t.Fatal("a misconfigured capacity must not block every message")
	}
}
