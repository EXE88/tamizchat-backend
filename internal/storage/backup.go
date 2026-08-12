package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// backupPrefix and backupExt name the files this package writes, so listing and
// pruning never touch anything an operator put in the folder themselves.
const (
	backupPrefix = "tamizchat-"
	backupExt    = ".db"
)

// Backup is one saved copy of the database.
type Backup struct {
	Name string
	Path string
	Size int64
	At   time.Time
}

// BackupTo writes a consistent copy of the database into dir and returns it.
//
// It uses SQLite's own VACUUM INTO rather than copying the file, because a
// running server is writing to it: a plain file copy can catch a half-written
// page or miss the write-ahead log entirely. VACUUM INTO produces a complete,
// already-compacted database, and it is safe while the server is serving.
func (s *Store) BackupTo(ctx context.Context, dir string) (Backup, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Backup{}, fmt.Errorf("create backup dir: %w", err)
	}

	name := backupPrefix + time.Now().Format("20060102-150405") + backupExt
	path := filepath.Join(dir, name)

	if _, err := os.Stat(path); err == nil {
		return Backup{}, fmt.Errorf("a backup with that name already exists: %s", name)
	}

	// The path goes into SQL text, so a quote in it would break the statement.
	// Backup directories are operator-controlled, but doubling the quote costs
	// nothing and removes the question.
	quoted := strings.ReplaceAll(path, "'", "''")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+quoted+"'"); err != nil {
		return Backup{}, fmt.Errorf("backup failed: %w", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return Backup{}, err
	}
	return Backup{Name: name, Path: path, Size: info.Size(), At: info.ModTime()}, nil
}

// ListBackups returns the backups in dir, newest first.
func ListBackups(dir string) ([]Backup, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read backup dir: %w", err)
	}

	var out []Backup
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, backupPrefix) || !strings.HasSuffix(name, backupExt) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, Backup{
			Name: name,
			Path: filepath.Join(dir, name),
			Size: info.Size(),
			At:   info.ModTime(),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// PruneBackups deletes all but the newest keep backups and reports how many
// went. Without it a nightly backup fills the disk in a year.
func PruneBackups(dir string, keep int) (int, error) {
	if keep < 1 {
		keep = 1
	}
	backups, err := ListBackups(dir)
	if err != nil {
		return 0, err
	}
	if len(backups) <= keep {
		return 0, nil
	}

	removed := 0
	for _, b := range backups[keep:] {
		if err := os.Remove(b.Path); err == nil {
			removed++
		}
	}
	return removed, nil
}

// RestoreFrom replaces the live database with a backup.
//
// The current database is renamed aside rather than deleted, so a restore from
// the wrong file is recoverable. The server must not be running: the caller
// checks that, because only it knows.
func RestoreFrom(dbPath, backupPath string) (rescued string, err error) {
	if err := verifyBackup(backupPath); err != nil {
		return "", err
	}

	if _, err := os.Stat(dbPath); err == nil {
		rescued = dbPath + ".replaced-" + time.Now().Format("20060102-150405")
		if err := os.Rename(dbPath, rescued); err != nil {
			return "", fmt.Errorf("could not set the current database aside: %w", err)
		}
	}

	if err := copyFile(backupPath, dbPath); err != nil {
		// Put things back the way they were rather than leaving no database.
		if rescued != "" {
			_ = os.Rename(rescued, dbPath)
		}
		return "", err
	}

	// The write-ahead log and shared-memory files belong to the database that
	// was just replaced; leaving them would corrupt the restored one.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
	return rescued, nil
}

// verifyBackup refuses to restore something that is not a usable database.
func verifyBackup(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("backup not found: %w", err)
	}
	if info.IsDir() || info.Size() == 0 {
		return fmt.Errorf("backup is not a database file")
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// Every SQLite file starts with this string.
	header := make([]byte, 16)
	if _, err := file.Read(header); err != nil {
		return fmt.Errorf("backup is not readable: %w", err)
	}
	if string(header[:15]) != "SQLite format 3" {
		return fmt.Errorf("this file is not a SQLite database")
	}
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}
	if err := os.WriteFile(dst, data, 0o640); err != nil {
		return fmt.Errorf("write database: %w", err)
	}
	return nil
}
