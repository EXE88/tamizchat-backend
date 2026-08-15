package httpapi

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPacedFileDeliversAtPlayingTime is the fix for the bug that stopped every
// music bot: a track handed to Ingress at full speed ended before its WebRTC
// connection came up, so nothing was ever published. The file must take roughly
// as long to send as it takes to play.
func TestPacedFileDeliversAtPlayingTime(t *testing.T) {
	// 60 KB that "plays" for 3 seconds: 20 KB/s, so 30 KB/s once the speedup is
	// applied. The burst is bigger than the whole file, so it is lowered here to
	// something a test can measure.
	path := filepath.Join(t.TempDir(), "track.bin")
	if err := os.WriteFile(path, make([]byte, 60<<10), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	paced := newPacedFile(context.Background(), file, 60<<10, 3*time.Second)
	paced.burst = 10 << 10

	started := time.Now()
	n, err := io.Copy(io.Discard, paced)
	took := time.Since(started)

	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != 60<<10 {
		t.Fatalf("copied %d bytes, want the whole file", n)
	}

	// The window is wide on purpose: what matters is "paced, not instant" and
	// "not slower than the track plays", never a precise rate.
	if took < time.Second {
		t.Fatalf("the file went out in %v — that is the unpaced behaviour", took)
	}
	if took > 4*time.Second {
		t.Fatalf("the file took %v, which is slower than it plays", took)
	}
}

// TestPacedFileWithoutADurationIsNotPaced keeps the fallback honest: a format
// whose length could not be worked out must still be served, at full speed.
func TestPacedFileWithoutADurationIsNotPaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.bin")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	started := time.Now()
	n, err := io.Copy(io.Discard, newPacedFile(context.Background(), file, 1<<20, 0))
	took := time.Since(started)

	if err != nil || n != 1<<20 {
		t.Fatalf("copy: %d bytes, %v", n, err)
	}
	if took > time.Second {
		t.Fatalf("an unknown duration must not throttle anything, took %v", took)
	}
}

// TestPacedFileStopsWhenTheFetcherGivesUp: a long track must not leave a
// goroutine sleeping through it after the puller has gone.
func TestPacedFileStopsWhenTheFetcherGivesUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.bin")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// An hour of playing time for a megabyte: without the cancellation this
	// copy would take most of that hour.
	paced := newPacedFile(ctx, file, 1<<20, time.Hour)
	paced.burst = 1 << 10

	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, paced)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the read should end with the context's error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the read kept going after the fetcher gave up")
	}
}
