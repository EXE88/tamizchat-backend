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
			p.warn("خواندن پوشهٔ بکاپ ناموفق بود: " + err.Error())
			return
		}

		p.clear()
		p.banner()
		p.printf("  %s\n", bold("بکاپ دیتابیس"))
		p.printf("  پوشه: %s\n\n", dir)

		if len(list) == 0 {
			p.println("  هنوز بکاپی گرفته نشده است.\n")
		} else {
			p.printf("  %-4s %-34s %-10s %s\n", "#", "نام", "حجم", "تاریخ")
			for i, b := range list {
				p.printf("  %-4d %-34s %-10s %s\n", i+1, b.Name,
					humanSize(b.Size), b.At.Format("2006-01-02 15:04"))
			}
			p.println("")
		}

		p.println("  n) گرفتن بکاپ تازه")
		p.println("  r) بازیابی از یک بکاپ")
		p.println("  0) بازگشت\n")

		switch p.ask("انتخاب کنید") {
		case "n":
			p.createBackup(ctx, dir)
		case "r":
			p.restoreBackup(list)
		case "0", "":
			return
		default:
			p.warn("گزینهٔ نامعتبر")
		}
	}
}

func (p *Panel) createBackup(ctx context.Context, dir string) {
	backup, err := p.store.BackupTo(ctx, dir)
	if err != nil {
		p.warn("بکاپ ناموفق بود: " + err.Error())
		return
	}

	message := fmt.Sprintf("بکاپ گرفته شد: %s (%s)", backup.Name, humanSize(backup.Size))
	if removed, err := storage.PruneBackups(dir, p.cfg.Int(config.KeyBackupKeep)); err == nil && removed > 0 {
		message += fmt.Sprintf(" — %d بکاپ قدیمی حذف شد", removed)
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
		p.warn("بکاپی برای بازیابی وجود ندارد")
		return
	}

	if _, running := p.control.Running(); running {
		p.warn("سرور در حال اجراست — اول آن را متوقف کنید، بعد بازیابی کنید")
		return
	}

	p.println("")
	n, err := strconv.Atoi(p.ask("شمارهٔ بکاپ"))
	if err != nil || n < 1 || n > len(list) {
		p.warn("شمارهٔ نامعتبر")
		return
	}
	chosen := list[n-1]

	p.printf("\n  دیتابیس فعلی با «%s» جایگزین می‌شود.\n", chosen.Name)
	p.println("  نسخهٔ فعلی حذف نمی‌شود و کنارش نگه داشته می‌شود.\n")
	if p.ask("مطمئنید؟ (بنویسید yes)") != "yes" {
		p.warn("لغو شد")
		return
	}

	// The panel holds the database open; the restore replaces that file.
	dbPath := p.store.Path()
	if err := p.store.Close(); err != nil {
		p.warn("بستن دیتابیس ناموفق بود: " + err.Error())
		return
	}

	rescued, err := storage.RestoreFrom(dbPath, chosen.Path)
	if err != nil {
		p.warn("بازیابی ناموفق بود: " + err.Error() +
			"\n  پنل را ببندید و دوباره باز کنید.")
		return
	}

	p.println("")
	p.printf("  %s\n", green("✓ بازیابی انجام شد"))
	if rescued != "" {
		p.printf("  نسخهٔ قبلی اینجاست: %s\n", rescued)
	}
	p.println("\n  حالا پنل را ببندید و دوباره باز کنید تا دیتابیس تازه خوانده شود.\n")
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
