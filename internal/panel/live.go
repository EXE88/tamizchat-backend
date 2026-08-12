package panel

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"tamizchat/internal/control"
)

// liveMenu is everything that only makes sense against a server that is
// actually running: who is connected, what it is doing, and the reload that
// makes the rest of this panel take effect without a restart.
func (p *Panel) liveMenu() {
	for {
		p.clear()
		p.banner()
		p.printf("  %s\n\n", bold("کنترل سرور در حال اجرا"))

		pid, running := p.control.Running()
		if !running {
			p.println("  سرور در حال اجرا نیست.\n")
			p.println("  تغییرات این پنل در دیتابیس ذخیره می‌شوند و دفعهٔ بعد که")
			p.println("  سرور بالا بیاید اعمال خواهند شد.\n")
			p.pause()
			return
		}
		p.printf("  وضعیت: %s (PID %d)\n\n", green("در حال اجرا"), pid)

		p.println("  1) وضعیت لحظه‌ای")
		p.println("  2) کاربران آنلاین")
		p.println("  3) اعمال تغییرات روی سرور در حال اجرا (reload)")
		p.println("  4) اخراج یک کاربر")
		p.println("  5) ارسال اعلان به همه")
		p.println("  6) توقف پخش بات‌ها")
		p.println("  0) بازگشت\n")

		switch p.ask("انتخاب کنید") {
		case "1":
			p.liveStatus()
		case "2":
			p.liveOnline()
		case "3":
			p.liveReload()
		case "4":
			p.liveKick()
		case "5":
			p.liveNotice()
		case "6":
			p.liveStopBots()
		case "0", "":
			return
		default:
			p.warn("گزینهٔ نامعتبر")
		}
	}
}

func (p *Panel) liveStatus() {
	status, err := p.control.Status()
	if err != nil {
		p.controlError(err)
		return
	}

	p.clear()
	p.banner()
	p.printf("  %s\n\n", bold("وضعیت لحظه‌ای"))
	p.printf("  نسخه          : %s\n", status.Version)
	p.printf("  PID           : %d\n", status.PID)
	p.printf("  مدت اجرا      : %s\n", humanDuration(status.UptimeSec))
	p.printf("  نام سرور      : %s\n", status.ServerName)
	p.printf("  آدرس گوش‌دادن : %s\n", status.ListenAddr)
	p.printf("  کاربر آنلاین  : %d\n", status.OnlineUsers)
	p.printf("  روم‌ها        : %d\n", status.Rooms)
	p.printf("  بات‌ها        : %d (در حال پخش: %d)\n", status.Bots, status.BotsPlaying)
	p.printf("  ویس/ویدیو     : %s\n", yesNo(status.MediaOK))
	p.printf("  گوروتین       : %d\n", status.Goroutines)
	p.printf("  حافظهٔ heap    : %d مگابایت\n\n", status.HeapMB)
	p.pause()
}

func (p *Panel) liveOnline() {
	users, err := p.control.Online()
	if err != nil {
		p.controlError(err)
		return
	}

	p.clear()
	p.banner()
	p.printf("  %s (%d نفر)\n\n", bold("کاربران آنلاین"), len(users))

	if len(users) == 0 {
		p.println("  هیچ‌کس آنلاین نیست.\n")
		p.pause()
		return
	}

	p.printf("  %-4s %-20s %-18s %-16s %-8s %s\n", "#", "نام", "روم", "رول", "مدت", "آدرس")
	for i, u := range users {
		room := u.RoomName
		if room == "" {
			room = "—"
		}
		name := u.Username
		if u.Muted {
			name += " (میوت)"
		}
		p.printf("  %-4d %-20s %-18s %-16s %-8s %s\n", i+1,
			truncate(name, 20), truncate(room, 18), truncate(u.Roles, 16),
			humanDuration(u.OnlineSec), u.Remote)
		p.printf("       %s\n", dim(u.ClientUUID))
	}
	p.println("")
	p.pause()
}

// liveReload is the point of this whole menu: everything else in the panel
// writes to the database, and this is what makes a running server notice.
func (p *Panel) liveReload() {
	result, err := p.control.Reload()
	if err != nil {
		p.controlError(err)
		return
	}
	p.ok(fmt.Sprintf("اعمال شد — %d تنظیم تغییرکرده، %d رول، %d روم، %d بات",
		result.Settings, result.Roles, result.Rooms, result.Bots))
}

func (p *Panel) liveKick() {
	users, err := p.control.Online()
	if err != nil {
		p.controlError(err)
		return
	}
	if len(users) == 0 {
		p.warn("هیچ‌کس آنلاین نیست")
		return
	}

	p.println("")
	for i, u := range users {
		p.printf("  %d) %s — %s\n", i+1, u.Username, dim(u.ClientUUID))
	}
	p.println("")

	n, err := strconv.Atoi(p.ask("شمارهٔ کاربر"))
	if err != nil || n < 1 || n > len(users) {
		p.warn("شمارهٔ نامعتبر")
		return
	}
	target := users[n-1]

	reason := p.ask("دلیل (اختیاری)")
	if err := p.control.Kick(target.ClientUUID, reason); err != nil {
		p.controlError(err)
		return
	}
	p.ok("کاربر «" + target.Username + "» اخراج شد")
}

func (p *Panel) liveNotice() {
	p.println("")
	text := p.ask("متن اعلان برای همهٔ کاربران")
	if text == "" {
		p.warn("لغو شد")
		return
	}
	if err := p.control.Notice(text); err != nil {
		p.controlError(err)
		return
	}
	p.ok("اعلان فرستاده شد")
}

func (p *Panel) liveStopBots() {
	if err := p.control.StopBots(""); err != nil {
		p.controlError(err)
		return
	}
	p.ok("پخش همهٔ بات‌ها متوقف شد")
}

// controlError explains a failed command, telling apart "the server is not
// running" from a real problem.
func (p *Panel) controlError(err error) {
	if errors.Is(err, control.ErrNotRunning) {
		p.warn("سرور در حال اجرا نیست — تغییرات در دیتابیس ذخیره شده‌اند")
		return
	}
	p.warn("خطا: " + err.Error())
}

// offerReload is shown after a change that a running server needs to be told
// about, so the operator is not left wondering why nothing happened.
func (p *Panel) offerReload() {
	if _, running := p.control.Running(); !running {
		return
	}
	if p.ask("سرور در حال اجراست — همین حالا اعمال شود؟ (y/n)") != "y" {
		return
	}
	if _, err := p.control.Reload(); err != nil {
		p.controlError(err)
		return
	}
	p.printf("  %s\n", green("روی سرور در حال اجرا اعمال شد"))
}

func humanDuration(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%dث", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dد", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dس %dد", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dروز %dس", int(d.Hours())/24, int(d.Hours())%24)
}

// green marks something that succeeded. It is not called ok, because that name
// is taken by the idiomatic second return value all over this package.
func green(s string) string { return "\033[32m" + s + "\033[0m" }
