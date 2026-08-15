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
	"tamizchat/internal/control"
	"tamizchat/internal/storage"
	"tamizchat/internal/version"
)

// Panel holds the state of one interactive session.
type Panel struct {
	store  *storage.Store
	cfg    *config.Config
	access *access.Manager
	// control talks to the server process if one is running, which is what
	// lets a change here take effect without a restart.
	control *control.Client
	in      *bufio.Reader
	out     *bufio.Writer
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
		store:   store,
		cfg:     cfg,
		access:  accessMgr,
		control: control.NewClient(dbPath),
		in:      bufio.NewReader(os.Stdin),
		out:     bufio.NewWriter(os.Stdout),
	}
	defer p.out.Flush()
	return p.mainMenu(ctx)
}

func (p *Panel) mainMenu(ctx context.Context) error {
	for {
		p.clear()
		p.banner()
		p.printf("  %s  TamizChat admin panel\n", bold("◆"))
		p.printf("  Database: %s\n\n", p.store.Path())

		p.printf("   1) Server status\n")
		p.printf("   2) Server settings\n")
		p.printf("   3) Show all settings\n")
		p.printf("   4) Reset a setting to its default\n")
		p.printf("   5) Known users\n")
		p.printf("   6) Rooms\n")
		p.printf("   7) Roles and permissions\n")
		p.printf("   8) Bans and mutes\n")
		p.printf("   9) Moderation log\n")
		p.printf("  10) Bots\n")
		p.printf("  11) Running server\n")
		p.printf("  12) systemd service\n")
		p.printf("  13) Backup and restore\n")
		p.printf("   0) Exit\n\n")

		switch p.ask("Choose") {
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
		case "10":
			p.botsMenu(ctx)
		case "11":
			p.liveMenu()
		case "12":
			p.serviceMenu()
		case "13":
			p.backupMenu(ctx)
		case "0", "q", "exit":
			p.println("")
			return nil
		default:
			p.warn("Invalid choice")
		}
	}
}

func (p *Panel) showStatus(ctx context.Context) {
	p.clear()
	p.banner()
	uuid, err := p.store.ServerUUID(ctx)
	if err != nil {
		p.warn("Could not read the server ID: " + err.Error())
		return
	}
	p.printf("  Version       : %s (%s)\n", version.Version, version.Commit)
	p.printf("  Server ID     : %s\n", uuid)
	p.printf("  Server name   : %s\n", p.cfg.String(config.KeyServerName))
	p.printf("  Listen address: %s\n", p.cfg.String(config.KeyListenAddr))
	p.printf("  Server passwd : %s\n", yesNo(p.cfg.String(config.KeyServerPassword) != ""))
	p.printf("  Voice/video   : %s\n", yesNo(p.cfg.Bool(config.KeyLiveKitEnabled)))
	p.printf("  File uploads  : %s\n", yesNo(p.cfg.Bool(config.KeyUploadsEnabled)))
	p.printf("  Database file : %s\n\n", p.store.Path())
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
		p.warn("Could not read users: " + err.Error())
		return
	}
	users, err := p.store.RecentUsers(ctx, 50)
	if err != nil {
		p.warn("Could not read users: " + err.Error())
		return
	}

	p.printf("  %s (total: %d)\n\n", bold("Known users"), total)
	if len(users) == 0 {
		p.println("  No user has connected yet.\n")
		p.pause()
		return
	}

	p.printf("  %-24s  %-38s  %-10s  %s\n", "NAME", "CLIENT ID", "VISITS", "LAST SEEN")
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
		p.println("  Pick a section:\n")
		for i, s := range config.Sections {
			p.printf("  %d) %s\n", i+1, sectionTitle(s))
		}
		p.println("  0) Back\n")

		choice := p.ask("Choose")
		if choice == "0" || choice == "" {
			return
		}
		n, err := strconv.Atoi(choice)
		if err != nil || n < 1 || n > len(config.Sections) {
			p.warn("Invalid choice")
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
		p.println("  0) Back\n")

		choice := p.ask("Number of the setting to edit")
		if choice == "0" || choice == "" {
			return
		}
		n, err := strconv.Atoi(choice)
		if err != nil || n < 1 || n > len(items) {
			p.warn("Invalid choice")
			continue
		}
		p.editSetting(ctx, items[n-1])
	}
}

func (p *Panel) editSetting(ctx context.Context, s config.Setting) {
	p.println("")
	p.printf("  %s\n", bold(s.Title))
	p.printf("  %s\n", dim(s.Help))
	p.printf("  Key          : %s\n", s.Key)
	p.printf("  Current value: %s\n", config.Display(s, p.cfg.String(s.Key)))
	p.printf("  Default      : %s\n", config.Display(s, s.Default))

	switch s.Kind {
	case config.KindBool:
		p.printf("  New value (true/false) — empty cancels\n")
	case config.KindEnum:
		p.printf("  Allowed values: %s\n", strings.Join(s.Options, " | "))
	}

	val := p.ask("New value")
	if val == "" {
		p.warn("Nothing changed")
		return
	}
	if err := p.cfg.Set(ctx, s.Key, val); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	if s.Key == config.KeyListenAddr {
		// The listener is bound once at startup; nothing short of a restart
		// can move it.
		p.ok("Saved — this setting only takes effect after a server restart")
		return
	}
	p.saved("Saved")
}

// saved reports a change and, when a server is running, offers to make it take
// effect immediately. Every panel edit writes to the database first, so
// declining just means it waits for the next restart.
func (p *Panel) saved(message string) {
	p.println("")
	p.printf("  %s\n", green("✓ "+message))
	p.offerReload()
	p.pause()
}

// okLive is saved() under the name the other menus call it by.
func (p *Panel) okLive(message string) { p.saved(message) }

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
	key := p.ask("Setting key (e.g. server.name)")
	if key == "" {
		return
	}
	if _, ok := config.Lookup(key); !ok {
		p.warn("No such key")
		return
	}
	if p.ask("Are you sure? (y/n)") != "y" {
		p.warn("Cancelled")
		return
	}
	if err := p.cfg.Reset(ctx, key); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.ok("Reset to default")
}

func sectionTitle(section string) string {
	switch section {
	case "server":
		return "General server settings"
	case "network":
		return "Network"
	case "users":
		return "Users"
	case "rooms":
		return "Rooms"
	case "chat":
		return "Chat"
	case "paint":
		return "Paint board"
	case "tls":
		return "TLS"
	case "backup":
		return "Backup"
	case "uploads":
		return "Files and images"
	case "livekit":
		return "Voice and video (LiveKit)"
	case "bots":
		return "Bots"
	case "log":
		return "Logging"
	}
	return section
}

func yesNo(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
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
	p.printf("  %s", dim("Press Enter to continue..."))
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
