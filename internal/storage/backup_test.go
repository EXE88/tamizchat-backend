package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

func TestBackupCapturesTheData(t *testing.T) {
	ctx := context.Background()
	store, _ := openTemp(t)

	if err := store.CreateRoom(ctx, Room{ID: NewUUID(), Name: "Lobby", Capacity: 5}); err != nil {
		t.Fatalf("create room: %v", err)
	}

	dir := t.TempDir()
	backup, err := store.BackupTo(ctx, dir)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if backup.Size == 0 {
		t.Fatal("the backup is empty")
	}

	// The copy must be a working database with the same contents, not just a
	// file of the right size.
	copied, err := Open(ctx, backup.Path)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer copied.Close()

	rooms, err := copied.ListRooms(ctx)
	if err != nil {
		t.Fatalf("list rooms in backup: %v", err)
	}
	if len(rooms) != 1 || rooms[0].Name != "Lobby" {
		t.Fatalf("the backup lost the data: %+v", rooms)
	}
}

func TestListAndPruneBackups(t *testing.T) {
	ctx := context.Background()
	store, _ := openTemp(t)
	dir := t.TempDir()

	// Backups are named by the second, so build the extra ones by hand to get
	// distinct files without sleeping through three seconds of test time.
	first, err := store.BackupTo(ctx, dir)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	data, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	for _, name := range []string{"tamizchat-20200101-000001.db", "tamizchat-20200101-000002.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o640); err != nil {
			t.Fatalf("write backup: %v", err)
		}
	}

	// Something the operator dropped in the folder must be left alone.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o640); err != nil {
		t.Fatalf("write note: %v", err)
	}

	list, err := ListBackups(dir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected three backups, got %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].At.Before(list[i].At) {
			t.Fatal("backups should be listed newest first")
		}
	}

	removed, err := PruneBackups(dir, 1)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected two removed, got %d", removed)
	}

	list, _ = ListBackups(dir)
	if len(list) != 1 {
		t.Fatalf("one backup should be kept, got %d", len(list))
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal("pruning must not touch files it did not write")
	}
}

func TestListBackupsOnAMissingFolder(t *testing.T) {
	list, err := ListBackups(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("a missing folder is not an error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected nothing, got %d", len(list))
	}
}

func TestRestoreReplacesTheDatabaseAndKeepsTheOldOne(t *testing.T) {
	ctx := context.Background()
	store, dbPath := openTemp(t)
	dir := t.TempDir()

	// Back up a database with one room, then add a second room to the live one.
	if err := store.CreateRoom(ctx, Room{ID: NewUUID(), Name: "Lobby", Capacity: 5}); err != nil {
		t.Fatalf("create room: %v", err)
	}
	backup, err := store.BackupTo(ctx, dir)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if err := store.CreateRoom(ctx, Room{ID: NewUUID(), Name: "Later", Capacity: 5}); err != nil {
		t.Fatalf("create room: %v", err)
	}
	store.Close()

	rescued, err := RestoreFrom(dbPath, backup.Path)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if rescued == "" {
		t.Fatal("the replaced database should be kept, not deleted")
	}
	if _, err := os.Stat(rescued); err != nil {
		t.Fatalf("the rescued file should exist: %v", err)
	}

	restored, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer restored.Close()

	rooms, err := restored.ListRooms(ctx)
	if err != nil {
		t.Fatalf("list rooms: %v", err)
	}
	if len(rooms) != 1 || rooms[0].Name != "Lobby" {
		t.Fatalf("the restore did not take: %+v", rooms)
	}
}

// Restoring the wrong file would destroy a working server, so anything that is
// not a database is refused before the live one is touched.
func TestRestoreRefusesSomethingThatIsNotADatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "live.db")
	if err := os.WriteFile(dbPath, []byte("original"), 0o640); err != nil {
		t.Fatalf("write db: %v", err)
	}

	for name, content := range map[string]string{
		"empty.db":   "",
		"garbage.db": "this is not a database",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := RestoreFrom(dbPath, path); err == nil {
			t.Fatalf("%s should have been refused", name)
		}
	}

	if _, err := RestoreFrom(dbPath, filepath.Join(dir, "missing.db")); err == nil {
		t.Fatal("a missing backup should be refused")
	}

	// The live database was never touched.
	data, err := os.ReadFile(dbPath)
	if err != nil || string(data) != "original" {
		t.Fatalf("the live database must be left alone: %q (%v)", data, err)
	}
}
