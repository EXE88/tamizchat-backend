package panel

import (
	"context"
	"strconv"
	"strings"

	"tamizchat/internal/bots"
	"tamizchat/internal/storage"
	"tamizchat/internal/textutil"
)

// botsMenu is where the server owner configures bots: a name and a folder of
// music is all a music bot needs. Moving it around and driving playback happens
// from the client, by anyone holding the bot-control permission.
func (p *Panel) botsMenu(ctx context.Context) {
	for {
		list, err := p.store.ListBots(ctx)
		if err != nil {
			p.warn("خواندن بات‌ها ناموفق بود: " + err.Error())
			return
		}

		p.clear()
		p.banner()
		p.printf("  %s (%d بات)\n\n", bold("بات‌ها"), len(list))

		if len(list) == 0 {
			p.println("  هنوز باتی ساخته نشده است.\n")
		} else {
			p.printf("  %-4s %-20s %-8s %-8s %s\n", "#", "نام", "وضعیت", "آهنگ", "فولدر")
			for i, b := range list {
				count, note := p.trackCount(b.Folder)
				p.printf("  %-4d %-20s %-8s %-8s %s\n", i+1, truncate(b.Name, 20),
					yesNo(b.Enabled), count, truncate(b.Folder, 40))
				if note != "" {
					p.printf("       %s\n", dim(note))
				}
			}
			p.println("")
		}

		p.println("  n) ساخت بات جدید")
		p.println("  e) ویرایش بات")
		p.println("  d) حذف بات")
		p.println("  0) بازگشت\n")

		switch p.ask("انتخاب کنید") {
		case "n":
			p.createBot(ctx)
		case "e":
			p.editBot(ctx, list)
		case "d":
			p.deleteBot(ctx, list)
		case "0", "":
			return
		default:
			p.warn("گزینهٔ نامعتبر")
		}
	}
}

// trackCount reports how much music a folder actually holds, so a typo in the
// path is visible here rather than as a silent failure at play time.
func (p *Panel) trackCount(folder string) (string, string) {
	n, err := bots.CountPlayable(folder)
	if err != nil {
		return "؟", "مشکل فولدر: " + err.Error()
	}
	if n == 0 {
		return "0", "هیچ فایل صوتی قابل پخشی در این فولدر پیدا نشد"
	}
	return strconv.Itoa(n), ""
}

func (p *Panel) createBot(ctx context.Context) {
	p.println("")
	name, ok := p.askBotName("نام بات")
	if !ok {
		return
	}

	folder := strings.TrimSpace(p.ask("مسیر فولدر موسیقی"))
	if folder == "" {
		p.warn("مسیر فولدر لازم است")
		return
	}
	if n, err := bots.CountPlayable(folder); err != nil {
		p.warn("این فولدر خوانده نشد: " + err.Error())
		return
	} else if n == 0 {
		p.warn("هیچ فایل صوتی در این فولدر نیست — بات ساخته می‌شود ولی چیزی برای پخش ندارد")
	}

	bot := storage.Bot{
		ID:        storage.NewUUID(),
		Name:      name,
		Kind:      storage.BotKindMusic,
		Folder:    folder,
		LoopQueue: true,
		Enabled:   true,
	}
	bot.Shuffle = p.ask("پخش تصادفی؟ (y/n)") == "y"

	if err := p.store.CreateBot(ctx, bot); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("بات ساخته شد — برای دیده‌شدن در سرورِ در حال اجرا، سرور را ری‌استارت کنید")
}

func (p *Panel) editBot(ctx context.Context, list []storage.Bot) {
	bot, ok := p.pickBot("شمارهٔ بات برای ویرایش", list)
	if !ok {
		return
	}

	p.printf("\n  ویرایش «%s» — هر فیلد را خالی بگذارید تا تغییر نکند\n", bot.Name)

	if raw := p.ask("نام جدید"); raw != "" {
		name, err := textutil.NormalizeName(raw, "نام بات", 1, 32)
		if err != nil {
			p.warn(err.Error())
			return
		}
		bot.Name = name
	}
	if raw := strings.TrimSpace(p.ask("فولدر جدید")); raw != "" {
		if _, err := bots.CountPlayable(raw); err != nil {
			p.warn("این فولدر خوانده نشد: " + err.Error())
			return
		}
		bot.Folder = raw
	}
	switch p.ask("پخش تصادفی؟ (y/n، خالی = بدون تغییر)") {
	case "y":
		bot.Shuffle = true
	case "n":
		bot.Shuffle = false
	}
	switch p.ask("تکرار صف؟ (y/n، خالی = بدون تغییر)") {
	case "y":
		bot.LoopQueue = true
	case "n":
		bot.LoopQueue = false
	}
	switch p.ask("فعال باشد؟ (y/n، خالی = بدون تغییر)") {
	case "y":
		bot.Enabled = true
	case "n":
		bot.Enabled = false
	}

	if err := p.store.UpdateBot(ctx, bot); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("ذخیره شد — روی سرورِ در حال اجرا پس از ری‌استارت اعمال می‌شود")
}

func (p *Panel) deleteBot(ctx context.Context, list []storage.Bot) {
	bot, ok := p.pickBot("شمارهٔ بات برای حذف", list)
	if !ok {
		return
	}
	if p.ask("حذف بات «"+bot.Name+"»؟ (y/n)") != "y" {
		p.warn("لغو شد")
		return
	}
	if err := p.store.DeleteBot(ctx, bot.ID); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("بات حذف شد")
}

func (p *Panel) pickBot(prompt string, list []storage.Bot) (storage.Bot, bool) {
	if len(list) == 0 {
		p.warn("باتی وجود ندارد")
		return storage.Bot{}, false
	}
	p.println("")
	n, err := strconv.Atoi(p.ask(prompt))
	if err != nil || n < 1 || n > len(list) {
		p.warn("شمارهٔ نامعتبر")
		return storage.Bot{}, false
	}
	return list[n-1], true
}

func (p *Panel) askBotName(prompt string) (string, bool) {
	name, err := textutil.NormalizeName(p.ask(prompt), "نام بات", 1, 32)
	if err != nil {
		p.warn(err.Error())
		return "", false
	}
	return name, true
}
