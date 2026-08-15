// Package bots runs the server's bots. Today that means music bots: a named
// participant that sits in a room and plays a folder of audio files.
//
// The audio itself never passes through this server. LiveKit's Ingress service
// pulls each track from an HTTP endpoint here — an ordinary file download — and
// does the transcoding and publishing. This server decides *what* plays and
// *where*; it does not touch a single sample.
package bots

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxTracks bounds a scan, so pointing a bot at a huge directory cannot spend
// the server's memory on filenames.
const maxTracks = 5000

// audioExtensions are the files a scan will pick up. Ingress transcodes them,
// so the list is about what is plausibly music, not about what Go can decode.
var audioExtensions = map[string]bool{
	".mp3":  true,
	".ogg":  true,
	".opus": true,
	".flac": true,
	".m4a":  true,
	".aac":  true,
	".wav":  true,
	".wma":  true,
}

// Track is one playable file.
type Track struct {
	// Title is the file name without its extension, which is what a listener
	// recognises.
	Title string
	// Path is absolute and stays on the server; it is never sent to a client.
	Path string
	Size int64
}

// scanFolder lists the playable files in a folder, sorted by name so the queue
// order matches what the operator sees in their file manager.
//
// Subfolders are included: people organise music by album.
func scanFolder(root string) ([]Track, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("the music folder path is empty")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("the folder path is not valid: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("could not open the music folder: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("the given path is not a folder")
	}

	var tracks []Track
	err = filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable corner of the tree should not lose the rest of the
			// music.
			return nil
		}
		if d.IsDir() || len(tracks) >= maxTracks {
			return nil
		}
		if !audioExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		stat, err := d.Info()
		if err != nil {
			return nil
		}
		tracks = append(tracks, Track{
			Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			Path:  path,
			Size:  stat.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("could not walk the folder: %w", err)
	}

	sort.Slice(tracks, func(i, j int) bool {
		return strings.ToLower(tracks[i].Path) < strings.ToLower(tracks[j].Path)
	})
	return tracks, nil
}

// CountPlayable reports how many playable files a folder holds. The admin panel
// uses it to tell an operator straight away that a path is wrong, instead of
// leaving them to discover it when the bot refuses to play.
func CountPlayable(folder string) (int, error) {
	tracks, err := scanFolder(folder)
	if err != nil {
		return 0, err
	}
	return len(tracks), nil
}

// withinRoot reports whether path really sits inside root. A bot's folder is
// configured by the operator, but the path that reaches the file server comes
// from an index into the scanned list — this is the belt-and-braces check that
// an index can never turn into an arbitrary file read.
func withinRoot(root, path string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	// Both sides are absolutized before they are compared. filepath.Rel refuses
	// to relate an absolute base to a relative path, and "data/bots" — the
	// default — is relative, so skipping this rejected every legitimate upload
	// on a stock configuration.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
