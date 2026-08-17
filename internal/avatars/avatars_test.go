package avatars

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 40, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestStoreSquaresAndScales(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Deliberately a wide picture, so the centre crop is doing something.
	token := m.Ticket("abc-123")
	uuid, version, err := m.Store(token, bytes.NewReader(pngBytes(t, 800, 400)))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if uuid != "abc-123" || version == "" {
		t.Fatalf("store returned %q %q", uuid, version)
	}
	if got := m.Version("abc-123"); got != version {
		t.Fatalf("version %q, want %q", got, version)
	}

	file, _, err := m.Open("abc-123")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()

	cfg, format, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("stored as %q, want jpeg — whatever arrives is re-encoded", format)
	}
	if cfg.Width != Size || cfg.Height != Size {
		t.Fatalf("stored %dx%d, want %dx%d", cfg.Width, cfg.Height, Size, Size)
	}
}

// A ticket is permission to upload exactly one picture. Reusing one would let a
// client that saw a token upload over somebody's face repeatedly.
func TestTicketIsSingleUse(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	token := m.Ticket("abc")
	if _, _, err := m.Store(token, bytes.NewReader(pngBytes(t, 64, 64))); err != nil {
		t.Fatalf("first store: %v", err)
	}
	if _, _, err := m.Store(token, bytes.NewReader(pngBytes(t, 64, 64))); err != ErrBadToken {
		t.Fatalf("second store: %v, want ErrBadToken", err)
	}
}

func TestRejectsThingsThatAreNotImages(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	body := strings.NewReader("<svg onload=\"alert(1)\"></svg>")
	if _, _, err := m.Store(m.Ticket("abc"), body); err != ErrNotImage {
		t.Fatalf("store: %v, want ErrNotImage", err)
	}
}

func TestOversizeIsRefusedBeforeDecoding(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	huge := io.LimitReader(zeroes{}, MaxUpload+2)
	if _, _, err := m.Store(m.Ticket("abc"), huge); err != ErrTooLarge {
		t.Fatalf("store: %v, want ErrTooLarge", err)
	}
}

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) { return len(p), nil }

// The id from a request goes straight into a filename, so it is the one place a
// client could try to climb out of the folder.
func TestOpenRefusesIdsThatAreNotUUIDs(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "..", "secret.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, id := range []string{"../secret", "..\\secret", "a/b", "", "with space", "name:stream"} {
		if _, _, err := m.Open(id); err != ErrNoAvatar {
			t.Fatalf("Open(%q) = %v, want ErrNoAvatar", id, err)
		}
	}
}

func TestRemoveForgetsTheVersion(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if _, _, err := m.Store(m.Ticket("abc"), bytes.NewReader(pngBytes(t, 64, 64))); err != nil {
		t.Fatalf("store: %v", err)
	}

	m.Remove("abc")

	if got := m.Version("abc"); got != "" {
		t.Fatalf("version after remove = %q, want empty", got)
	}
	if _, _, err := m.Open("abc"); err != ErrNoAvatar {
		t.Fatalf("open after remove: %v, want ErrNoAvatar", err)
	}

	// Removing again is not an error: the caller wanted them to have none.
	m.Remove("abc")
}

// A picture has to survive a restart, and the tag has to survive with it — a
// client that cached the old tag must not be told the picture is unchanged when
// the server has forgotten what it was.
func TestVersionsAreRebuiltFromDisk(t *testing.T) {
	dir := t.TempDir()

	first, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	_, version, err := first.Store(first.Ticket("abc"), bytes.NewReader(pngBytes(t, 64, 64)))
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	second, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := second.Version("abc"); got != version {
		t.Fatalf("version after restart = %q, want %q", got, version)
	}
}
