package panel

import (
	"context"
	"strconv"
	"strings"

	"tamizchat/internal/bots"
	"tamizchat/internal/config"
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
			p.warn("Could not read bots: " + err.Error())
			return
		}

		p.clear()
		p.banner()
		p.printf("  %s (%d)\n\n", bold("Bots"), len(list))

		if len(list) == 0 {
			p.println("  No bots have been created yet.\n")
		} else {
			p.printf("  %-4s %-20s %-10s %-8s %s\n", "#", "NAME", "STATE", "TRACKS", "PLAYS FROM")
			for i, b := range list {
				source, label := p.botSource(ctx, b)
				count, note := p.trackCount(source)
				p.printf("  %-4d %-20s %-10s %-8s %s\n", i+1, truncate(b.Name, 20),
					yesNo(b.Enabled), count, truncate(label, 40))
				if note != "" {
					p.printf("       %s\n", dim(note))
				}
			}
			p.println("")
		}

		p.println("  n) Create a bot")
		p.println("  e) Edit a bot")
		p.println("  d) Delete a bot")
		p.println("  0) Back\n")

		switch p.ask("Choose") {
		case "n":
			p.createBot(ctx)
		case "e":
			p.editBot(ctx, list)
		case "d":
			p.deleteBot(ctx, list)
		case "0", "":
			return
		default:
			p.warn("Invalid choice")
		}
	}
}

// botSource is where a bot actually plays from, and how to describe it. A bot
// on a playlist plays that playlist's folder, not the one in its own row, and
// reporting the row would tell the operator a bot with music has none.
func (p *Panel) botSource(ctx context.Context, b storage.Bot) (folder, label string) {
	if b.PlaylistID == "" {
		return b.Folder, b.Folder
	}

	folder = bots.PlaylistFolder(p.cfg.String(config.KeyBotsDir), p.store.Path(), b.ID, b.PlaylistID)

	name := b.PlaylistID
	if list, err := p.store.GetPlaylist(ctx, b.PlaylistID); err == nil {
		name = list.Name
	}
	return folder, "playlist: " + name
}

// trackCount reports how much music a folder actually holds, so a typo in the
// path is visible here rather than as a silent failure at play time.
func (p *Panel) trackCount(folder string) (string, string) {
	n, err := bots.CountPlayable(folder)
	if err != nil {
		return "?", "folder problem: " + err.Error()
	}
	if n == 0 {
		return "0", "no playable audio file was found in this folder"
	}
	return strconv.Itoa(n), ""
}

func (p *Panel) createBot(ctx context.Context) {
	p.println("")
	name, ok := p.askBotName("Bot name")
	if !ok {
		return
	}

	folder := strings.TrimSpace(p.ask("Music folder path"))
	if folder == "" {
		p.warn("The folder path is required")
		return
	}
	if n, err := bots.CountPlayable(folder); err != nil {
		p.warn("Could not read that folder: " + err.Error())
		return
	} else if n == 0 {
		p.warn("No audio files in that folder — the bot will be created but has nothing to play")
	}

	bot := storage.Bot{
		ID:        storage.NewUUID(),
		Name:      name,
		Kind:      storage.BotKindMusic,
		Folder:    folder,
		LoopQueue: true,
		Enabled:   true,
	}
	bot.Shuffle = p.ask("Shuffle playback? (y/n)") == "y"

	if err := p.store.CreateBot(ctx, bot); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.okLive("Bot created")
}

func (p *Panel) editBot(ctx context.Context, list []storage.Bot) {
	bot, ok := p.pickBot("Number of the bot to edit", list)
	if !ok {
		return
	}

	p.printf("\n  Editing \"%s\" — leave a field empty to keep it unchanged\n", bot.Name)

	if raw := p.ask("New name"); raw != "" {
		name, err := textutil.NormalizeName(raw, "bot name", 1, 32)
		if err != nil {
			p.warn(err.Error())
			return
		}
		bot.Name = name
	}
	if raw := strings.TrimSpace(p.ask("New folder")); raw != "" {
		if _, err := bots.CountPlayable(raw); err != nil {
			p.warn("Could not read that folder: " + err.Error())
			return
		}
		bot.Folder = raw
	}
	switch p.ask("Shuffle playback? (y/n, empty = unchanged)") {
	case "y":
		bot.Shuffle = true
	case "n":
		bot.Shuffle = false
	}
	switch p.ask("Loop the queue? (y/n, empty = unchanged)") {
	case "y":
		bot.LoopQueue = true
	case "n":
		bot.LoopQueue = false
	}
	switch p.ask("Enabled? (y/n, empty = unchanged)") {
	case "y":
		bot.Enabled = true
	case "n":
		bot.Enabled = false
	}

	if err := p.store.UpdateBot(ctx, bot); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.okLive("Saved")
}

func (p *Panel) deleteBot(ctx context.Context, list []storage.Bot) {
	bot, ok := p.pickBot("Number of the bot to delete", list)
	if !ok {
		return
	}
	if p.ask("Delete bot \""+bot.Name+"\"? (y/n)") != "y" {
		p.warn("Cancelled")
		return
	}
	if err := p.store.DeleteBot(ctx, bot.ID); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.okLive("Bot deleted")
}

func (p *Panel) pickBot(prompt string, list []storage.Bot) (storage.Bot, bool) {
	if len(list) == 0 {
		p.warn("There are no bots")
		return storage.Bot{}, false
	}
	p.println("")
	n, err := strconv.Atoi(p.ask(prompt))
	if err != nil || n < 1 || n > len(list) {
		p.warn("Invalid number")
		return storage.Bot{}, false
	}
	return list[n-1], true
}

func (p *Panel) askBotName(prompt string) (string, bool) {
	name, err := textutil.NormalizeName(p.ask(prompt), "bot name", 1, 32)
	if err != nil {
		p.warn(err.Error())
		return "", false
	}
	return name, true
}
