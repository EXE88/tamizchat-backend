package bots

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestScanPicksUpAudioAndIgnoresTheRest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "song.mp3")
	write(t, dir, "tune.OGG") // the extension check is case-insensitive
	write(t, dir, "cover.jpg")
	write(t, dir, "notes.txt")

	tracks, err := scanFolder(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("expected the two audio files, got %+v", tracks)
	}
	for _, tr := range tracks {
		if filepath.Ext(tr.Title) != "" {
			t.Fatalf("the title should drop the extension, got %q", tr.Title)
		}
	}
}

// People organise music in album folders, so a scan has to go down the tree.
func TestScanIncludesSubfolders(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.mp3")
	write(t, dir, filepath.Join("album", "b.mp3"))
	write(t, dir, filepath.Join("album", "disc2", "c.mp3"))

	tracks, err := scanFolder(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(tracks) != 3 {
		t.Fatalf("expected three tracks, got %d", len(tracks))
	}

	// Sorted by path, so the queue matches what a file manager shows.
	for i := 1; i < len(tracks); i++ {
		if tracks[i-1].Path > tracks[i].Path {
			t.Fatalf("tracks are not sorted: %q before %q", tracks[i-1].Path, tracks[i].Path)
		}
	}
}

func TestScanRejectsBadFolders(t *testing.T) {
	if _, err := scanFolder(""); err == nil {
		t.Fatal("an empty path should be an error")
	}
	if _, err := scanFolder(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("a missing folder should be an error")
	}

	file := write(t, t.TempDir(), "a.mp3")
	if _, err := scanFolder(file); err == nil {
		t.Fatal("a file is not a folder")
	}
}

func TestEmptyFolderScansCleanly(t *testing.T) {
	n, err := CountPlayable(t.TempDir())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no tracks, got %d", n)
	}
}

// The stream endpoint turns a queue index into a path on disk. This is the
// check that no path outside the configured folder can ever be served.
func TestWithinRoot(t *testing.T) {
	root := t.TempDir()
	inside := write(t, root, filepath.Join("album", "song.mp3"))
	outside := write(t, t.TempDir(), "other.mp3")

	if !withinRoot(root, inside) {
		t.Fatal("a file in a subfolder is inside the root")
	}
	if withinRoot(root, outside) {
		t.Fatal("a file in another folder is not")
	}
	if withinRoot(root, filepath.Join(root, "..", "escape.mp3")) {
		t.Fatal("a path climbing out of the root must be refused")
	}
}
