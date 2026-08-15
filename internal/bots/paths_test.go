package bots

import (
	"path/filepath"
	"testing"
)

// TestWithinRootHandlesRelativePaths pins the bug that a relative root — which
// "data/bots", the default, is — must still work. filepath.Rel refuses to
// relate an absolute base to a relative path, so an unabsolutized comparison
// rejected every upload on a stock configuration while passing every test that
// used a temporary directory.
func TestWithinRootHandlesRelativePaths(t *testing.T) {
	cases := []struct {
		name string
		root string
		path string
		want bool
	}{
		{"relative root and path", "data/bots/one", filepath.Join("data/bots/one", "song.mp3"), true},
		{"relative root, absolute path", "data/bots/one", abs(t, "data/bots/one/song.mp3"), true},
		{"absolute root and path", abs(t, "data/bots/one"), abs(t, "data/bots/one/song.mp3"), true},
		{"a sibling folder is outside", "data/bots/one", filepath.Join("data/bots/two", "song.mp3"), false},
		{"climbing out is outside", "data/bots/one", filepath.Join("data/bots/one", "..", "song.mp3"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinRoot(tc.root, tc.path); got != tc.want {
				t.Fatalf("withinRoot(%q, %q) = %v, want %v", tc.root, tc.path, got, tc.want)
			}
		})
	}
}

func abs(t *testing.T, path string) string {
	t.Helper()
	out, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return out
}
