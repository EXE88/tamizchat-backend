package panel

import (
	"context"
	"fmt"
	"strconv"

	"tamizchat/internal/config"
	"tamizchat/internal/storage"
)

// backupMenu handles copies of the database: the rooms, roles, bans and bots.
// Room contents are never in there — they are temporary by design — so a backup
// is small and quick even on a busy server.
func (p *Panel) backupMenu(ctx context.Context) {
	for {
		dir := p.cfg.String(config.KeyBackupDir)
		list, err := storage.ListBackups(dir)
		if err != nil {
			p.warn("Could not read the backup folder: " + err.Error())
			return
		}

		p.clear()
		p.banner()
		p.printf("  %s\n", bold("Database backups"))
		p.printf("  Folder: %s\n\n", dir)

		if len(list) == 0 {
			p.println("  No backup has been taken yet.\n")
		} else {
			p.printf("  %-4s %-34s %-10s %s\n", "#", "NAME", "SIZE", "DATE")
			for i, b := range list {
				p.printf("  %-4d %-34s %-10s %s\n", i+1, b.Name,
					humanSize(b.Size), b.At.Format("2006-01-02 15:04"))
			}
			p.println("")
		}

		p.println("  n) Take a new backup")
		p.println("  r) Restore from a backup")
		p.println("  0) Back\n")

		switch p.ask("Choose") {
		case "n":
			p.createBackup(ctx, dir)
		case "r":
			p.restoreBackup(list)
		case "0", "":
			return
		default:
			p.warn("Invalid choice")
		}
	}
}

func (p *Panel) createBackup(ctx context.Context, dir string) {
	backup, err := p.store.BackupTo(ctx, dir)
	if err != nil {
		p.warn("Backup failed: " + err.Error())
		return
	}

	message := fmt.Sprintf("Backup taken: %s (%s)", backup.Name, humanSize(backup.Size))
	if removed, err := storage.PruneBackups(dir, p.cfg.Int(config.KeyBackupKeep)); err == nil && removed > 0 {
		message += fmt.Sprintf(" — %d old backup(s) removed", removed)
	}

	p.println("")
	p.printf("  %s\n", green("✓ "+message))
	p.pause()
}

// restoreBackup replaces the live database. It refuses while the server is
// running: swapping the file under an open connection corrupts it, and the
// server would keep serving the old data from memory anyway.
func (p *Panel) restoreBackup(list []storage.Backup) {
	if len(list) == 0 {
		p.warn("There is no backup to restore from")
		return
	}

	if _, running := p.control.Running(); running {
		p.warn("The server is running — stop it first, then restore")
		return
	}

	p.println("")
	n, err := strconv.Atoi(p.ask("Backup number"))
	if err != nil || n < 1 || n > len(list) {
		p.warn("Invalid number")
		return
	}
	chosen := list[n-1]

	p.printf("\n  The current database will be replaced with \"%s\".\n", chosen.Name)
	p.println("  The current copy is not deleted; it is kept alongside.\n")
	if p.ask("Are you sure? (type yes)") != "yes" {
		p.warn("Cancelled")
		return
	}

	// The panel holds the database open; the restore replaces that file.
	dbPath := p.store.Path()
	if err := p.store.Close(); err != nil {
		p.warn("Could not close the database: " + err.Error())
		return
	}

	rescued, err := storage.RestoreFrom(dbPath, chosen.Path)
	if err != nil {
		p.warn("Restore failed: " + err.Error() +
			"\n  Close the panel and open it again.")
		return
	}

	p.println("")
	p.printf("  %s\n", green("✓ Restore complete"))
	if rescued != "" {
		p.printf("  The previous copy is here: %s\n", rescued)
	}
	p.println("\n  Now close the panel and open it again so the new database is read.\n")
	p.pause()
}

func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGT"[exp])
}
