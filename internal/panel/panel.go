// Package panel implements the interactive command-line administration panel.
// Running the bare `tamizchat` command on the server opens it, in the spirit of
// x-ui: a numbered menu you drive with the keyboard, no .env file to hand-edit.
package panel

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"tamizchat/internal/access"
	"tamizchat/internal/config"
	"tamizchat/internal/storage"
	"tamizchat/internal/version"
)

// Panel holds the state of one interactive session.
type Panel struct {
	store  *storage.Store
	cfg    *config.Config
	access *access.Manager
	in     *bufio.Reader
	out    *bufio.Writer
}

// Run opens the database and drives the menu loop until the user exits.
func Run(ctx context.Context, dbPath string) error {
	store, err := storage.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	cfg, err := config.Load(ctx, store)
	if err != nil {
		return err
	}

	accessMgr, err := access.New(ctx, store)
	if err != nil {
		return err
	}

	p := &Panel{
		store:  store,
		cfg:    cfg,
		access: accessMgr,
		in:     bufio.NewReader(os.Stdin),
		out:    bufio.NewWriter(os.Stdout),
	}
	defer p.out.Flush()
	return p.mainMenu(ctx)
}

func (p *Panel) mainMenu(ctx context.Context) error {
	for {
		p.clear()
		p.banner()
		p.printf("  %s  پنل مدیریت TamizChat\n", bold("◆"))
		p.printf("  دیتابیس: %s\n\n", p.store.Path())

		p.printf("  1) وضعیت سرور\n")
		p.printf("  2) تنظیمات سرور\n")
		p.printf("  3) نمایش همهٔ تنظیمات\n")
		p.printf("  4) بازگرداندن یک تنظیم به مقدار پیش‌فرض\n")
		p.printf("  5) کاربران شناخته‌شده\n")
		p.printf("  6) مدیریت روم‌ها\n")
		p.printf("  7) رول‌ها و دسترسی‌ها\n")
		p.printf("  8) بن‌ها و میوت‌ها\n")
		p.printf("  9) لاگ اقدامات مدیریتی\n")
		p.printf("  0) خروج\n\n")

		switch p.ask("انتخاب کنید") {
		case "1":
			p.showStatus(ctx)
		case "2":
			p.sectionsMenu(ctx)
		case "3":
			p.showAll()
		case "4":
			p.resetSetting(ctx)
		case "5":
			p.showUsers(ctx)
		case "6":
			p.roomsMenu(ctx)
		case "7":
			p.accessMenu(ctx)
		case "8":
			p.sanctionsMenu(ctx)
		case "9":
			p.showModLog(ctx)
		case "0", "q", "exit":
			p.println("")
			return nil
		default:
			p.warn("گزینهٔ نامعتبر")
		}
	}
}

func (p *Panel) showStatus(ctx context.Context) {
	p.clear()
	p.banner()
	uuid, err := p.store.ServerUUID(ctx)
	if err != nil {
		p.warn("خواندن شناسهٔ سرور ناموفق بود: " + err.Error())
		return
	}
	p.printf("  نسخه         : %s (%s)\n", version.Version, version.Commit)
	p.printf("  شناسهٔ سرور   : %s\n", uuid)
	p.printf("  نام سرور     : %s\n", p.cfg.String(config.KeyServerName))
	p.printf("  آدرس اجرا    : %s\n", p.cfg.String(config.KeyListenAddr))
	p.printf("  رمز سرور     : %s\n", yesNo(p.cfg.String(config.KeyServerPassword) != ""))
	p.printf("  ویس/ویدیو    : %s\n", yesNo(p.cfg.Bool(config.KeyLiveKitEnabled)))
	p.printf("  ارسال فایل   : %s\n", yesNo(p.cfg.Bool(config.KeyUploadsEnabled)))
	p.printf("  فایل دیتابیس : %s\n\n", p.store.Path())
	p.pause()
}

// showUsers lists clients that have connected at least once. The list of who is
// online *right now* lives in the running server's memory and needs the control
// socket coming in phase 10.
func (p *Panel) showUsers(ctx context.Context) {
	p.clear()
	p.banner()

	total, err := p.store.CountUsers(ctx)
	if err != nil {
		p.warn("خواندن کاربران ناموفق بود: " + err.Error())
		return
	}
	users, err := p.store.RecentUsers(ctx, 50)
	if err != nil {
		p.warn("خواندن کاربران ناموفق بود: " + err.Error())
		return
	}

	p.printf("  %s (مجموع: %d)\n\n", bold("کاربران شناخته‌شده"), total)
	if len(users) == 0 {
		p.println("  هنوز هیچ کاربری وصل نشده است.\n")
		p.pause()
		return
	}

	p.printf("  %-24s  %-38s  %-10s  %s\n", "نام", "شناسهٔ کلاینت", "بازدید", "آخرین حضور")
	for _, u := range users {
		p.printf("  %-24s  %-38s  %-10d  %s\n",
			truncate(u.Username, 24), u.ClientUUID, u.VisitCount,
			time.Unix(u.LastSeenAt, 0).Format("2006-01-02 15:04"))
	}
	p.println("")
	p.pause()
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func (p *Panel) sectionsMenu(ctx context.Context) {
	for {
		p.clear()
		p.banner()
		p.println("  بخش مورد نظر را انتخاب کنید:\n")
		for i, s := range config.Sections {
			p.printf("  %d) %s\n", i+1, sectionTitle(s))
		}
		p.println("  0) بازگشت\n")

		choice := p.ask("انتخاب کنید")
		if choice == "0" || choice == "" {
			return
		}
		n, err := strconv.Atoi(choice)
		if err != nil || n < 1 || n > len(config.Sections) {
			p.warn("گزینهٔ نامعتبر")
			continue
		}
		p.sectionMenu(ctx, config.Sections[n-1])
	}
}

func (p *Panel) sectionMenu(ctx context.Context, section string) {
	for {
		items := config.BySection(section)
		p.clear()
		p.banner()
		p.printf("  %s\n\n", bold(sectionTitle(section)))
		for i, s := range items {
			p.printf("  %d) %-28s %s\n", i+1, s.Title, dim(config.Display(s, p.cfg.String(s.Key))))
		}
		p.println("  0) بازگشت\n")

		choice := p.ask("شمارهٔ تنظیم برای ویرایش")
		if choice == "0" || choice == "" {
			return
		}
		n, err := strconv.Atoi(choice)
		if err != nil || n < 1 || n > len(items) {
			p.warn("گزینهٔ نامعتبر")
			continue
		}
		p.editSetting(ctx, items[n-1])
	}
}

func (p *Panel) editSetting(ctx context.Context, s config.Setting) {
	p.println("")
	p.printf("  %s\n", bold(s.Title))
	p.printf("  %s\n", dim(s.Help))
	p.printf("  کلید        : %s\n", s.Key)
	p.printf("  مقدار فعلی  : %s\n", config.Display(s, p.cfg.String(s.Key)))
	p.printf("  پیش‌فرض     : %s\n", config.Display(s, s.Default))

	switch s.Kind {
	case config.KindBool:
		p.printf("  مقدار جدید (true/false) — خالی یعنی انصراف\n")
	case config.KindEnum:
		p.printf("  مقادیر مجاز : %s\n", strings.Join(s.Options, " | "))
	}

	val := p.ask("مقدار جدید")
	if val == "" {
		p.warn("تغییری اعمال نشد")
		return
	}
	if err := p.cfg.Set(ctx, s.Key, val); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("ذخیره شد")
	if s.Key == config.KeyListenAddr {
		p.warn("این تنظیم پس از ری‌استارت سرور اعمال می‌شود")
	}
}

func (p *Panel) showAll() {
	p.clear()
	p.banner()
	current := ""
	for _, s := range config.All() {
		if s.Section != current {
			current = s.Section
			p.printf("\n  %s\n", bold(sectionTitle(current)))
		}
		p.printf("    %-32s %s\n", s.Key, config.Display(s, p.cfg.String(s.Key)))
	}
	p.println("")
	p.pause()
}

func (p *Panel) resetSetting(ctx context.Context) {
	p.println("")
	key := p.ask("کلید تنظیم (مثلاً server.name)")
	if key == "" {
		return
	}
	if _, ok := config.Lookup(key); !ok {
		p.warn("چنین کلیدی وجود ندارد")
		return
	}
	if p.ask("مطمئنید؟ (y/n)") != "y" {
		p.warn("لغو شد")
		return
	}
	if err := p.cfg.Reset(ctx, key); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("به مقدار پیش‌فرض بازگشت")
}

func sectionTitle(section string) string {
	switch section {
	case "server":
		return "تنظیمات عمومی سرور"
	case "network":
		return "شبکه"
	case "users":
		return "کاربران"
	case "rooms":
		return "روم‌ها"
	case "chat":
		return "چت"
	case "paint":
		return "تختهٔ نقاشی"
	case "uploads":
		return "فایل و عکس"
	case "livekit":
		return "ویس و ویدیو (LiveKit)"
	case "log":
		return "لاگ"
	}
	return section
}

func yesNo(b bool) string {
	if b {
		return "فعال"
	}
	return "غیرفعال"
}

func (p *Panel) printf(format string, args ...any) {
	fmt.Fprintf(p.out, format, args...)
	p.out.Flush()
}

func (p *Panel) println(s string) { p.printf("%s\n", s) }

func (p *Panel) ask(prompt string) string {
	p.printf("  %s: ", prompt)
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		return "0" // stdin closed: behave like "back/exit"
	}
	return strings.TrimSpace(line)
}

func (p *Panel) pause() {
	p.printf("  %s", dim("برای ادامه Enter بزنید..."))
	p.in.ReadString('\n')
}

func (p *Panel) warn(msg string) {
	p.printf("\n  \033[33m! %s\033[0m\n", msg)
	p.pause()
}

func (p *Panel) ok(msg string) {
	p.printf("\n  \033[32m✓ %s\033[0m\n", msg)
	p.pause()
}

func (p *Panel) clear() { p.printf("\033[H\033[2J") }

func (p *Panel) banner() {
	p.printf("\n  \033[36m%s\033[0m  \033[2mv%s\033[0m\n\n", "TamizChat", version.Version)
}

func bold(s string) string { return "\033[1m" + s + "\033[0m" }
func dim(s string) string  { return "\033[2m" + s + "\033[0m" }
