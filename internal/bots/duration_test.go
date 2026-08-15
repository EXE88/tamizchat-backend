package bots

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTrackDurationReadsHeaders checks the formats the pacing depends on. A
// wrong answer here does not corrupt anything — it serves a track faster or
// slower than it plays — but "faster" is exactly the failure that stopped bots
// publishing anything at all, so it is worth pinning.
func TestTrackDurationReadsHeaders(t *testing.T) {
	dir := t.TempDir()

	t.Run("wav", func(t *testing.T) {
		// 48 kHz, mono, 16-bit: 96000 bytes per second. 3 seconds of it.
		path := filepath.Join(dir, "tone.wav")
		writeWAV(t, path, 48000, 1, 16, 3*48000*2)

		got, ok := TrackDuration(path)
		if !ok {
			t.Fatal("a wav header should be readable")
		}
		if got < 2900*time.Millisecond || got > 3100*time.Millisecond {
			t.Fatalf("duration = %v, want about 3s", got)
		}
	})

	t.Run("an unknown extension is not paced", func(t *testing.T) {
		path := filepath.Join(dir, "mystery.aac")
		if err := os.WriteFile(path, []byte("not really aac"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		if _, ok := TrackDuration(path); ok {
			t.Fatal("an unparsed format must report no duration, so it is served unpaced")
		}
	})

	t.Run("a truncated file is not paced", func(t *testing.T) {
		path := filepath.Join(dir, "broken.ogg")
		if err := os.WriteFile(path, []byte("OggS"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		if _, ok := TrackDuration(path); ok {
			t.Fatal("a file too short to hold a header must not claim a duration")
		}
	})

	t.Run("a missing file is not paced", func(t *testing.T) {
		if _, ok := TrackDuration(filepath.Join(dir, "gone.mp3")); ok {
			t.Fatal("a missing file must not claim a duration")
		}
	})
}

// writeWAV writes a silent PCM wav of the given size, header included.
func writeWAV(t *testing.T, path string, rate, channels, bits int, dataBytes int) {
	t.Helper()

	byteRate := rate * channels * bits / 8
	head := make([]byte, 44)

	copy(head[0:], "RIFF")
	binary.LittleEndian.PutUint32(head[4:], uint32(36+dataBytes))
	copy(head[8:], "WAVE")
	copy(head[12:], "fmt ")
	binary.LittleEndian.PutUint32(head[16:], 16)
	binary.LittleEndian.PutUint16(head[20:], 1)
	binary.LittleEndian.PutUint16(head[22:], uint16(channels))
	binary.LittleEndian.PutUint32(head[24:], uint32(rate))
	binary.LittleEndian.PutUint32(head[28:], uint32(byteRate))
	binary.LittleEndian.PutUint16(head[32:], uint16(channels*bits/8))
	binary.LittleEndian.PutUint16(head[34:], uint16(bits))
	copy(head[36:], "data")
	binary.LittleEndian.PutUint32(head[40:], uint32(dataBytes))

	if err := os.WriteFile(path, append(head, make([]byte, dataBytes)...), 0o600); err != nil {
		t.Fatalf("write wav: %v", err)
	}
}
