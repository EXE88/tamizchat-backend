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
		p.printf("  %s\n\n", bold("Running server"))

		pid, running := p.control.Running()
		if !running {
			p.println("  The server is not running.\n")
			p.println("  Changes made in this panel are stored in the database and")
			p.println("  will take effect the next time the server starts.\n")
			p.pause()
			return
		}
		p.printf("  Status: %s (PID %d)\n\n", green("running"), pid)

		p.println("  1) Live status")
		p.println("  2) Online users")
		p.println("  3) Apply changes to the running server (reload)")
		p.println("  4) Kick a user")
		p.println("  5) Broadcast a notice")
		p.println("  6) Stop bot playback")
		p.println("  0) Back\n")

		switch p.ask("Choose") {
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
			p.warn("Invalid choice")
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
	p.printf("  %s\n\n", bold("Live status"))
	p.printf("  Version       : %s\n", status.Version)
	p.printf("  PID           : %d\n", status.PID)
	p.printf("  Uptime        : %s\n", humanDuration(status.UptimeSec))
	p.printf("  Server name   : %s\n", status.ServerName)
	p.printf("  Listen address: %s\n", status.ListenAddr)
	p.printf("  Online users  : %d\n", status.OnlineUsers)
	p.printf("  Rooms         : %d\n", status.Rooms)
	p.printf("  Bots          : %d (playing: %d)\n", status.Bots, status.BotsPlaying)
	p.printf("  Voice/video   : %s\n", yesNo(status.MediaOK))
	p.printf("  Goroutines    : %d\n", status.Goroutines)
	p.printf("  Heap memory   : %d MB\n\n", status.HeapMB)
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
	p.printf("  %s (%d)\n\n", bold("Online users"), len(users))

	if len(users) == 0 {
		p.println("  Nobody is online.\n")
		p.pause()
		return
	}

	p.printf("  %-4s %-20s %-18s %-16s %-8s %s\n", "#", "NAME", "ROOM", "ROLES", "ONLINE", "ADDRESS")
	for i, u := range users {
		room := u.RoomName
		if room == "" {
			room = "—"
		}
		name := u.Username
		if u.Muted {
			name += " (muted)"
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
	p.ok(fmt.Sprintf("Applied — %d settings changed, %d roles, %d rooms, %d bots",
		result.Settings, result.Roles, result.Rooms, result.Bots))
}

func (p *Panel) liveKick() {
	users, err := p.control.Online()
	if err != nil {
		p.controlError(err)
		return
	}
	if len(users) == 0 {
		p.warn("Nobody is online")
		return
	}

	p.println("")
	for i, u := range users {
		p.printf("  %d) %s — %s\n", i+1, u.Username, dim(u.ClientUUID))
	}
	p.println("")

	n, err := strconv.Atoi(p.ask("User number"))
	if err != nil || n < 1 || n > len(users) {
		p.warn("Invalid number")
		return
	}
	target := users[n-1]

	reason := p.ask("Reason (optional)")
	if err := p.control.Kick(target.ClientUUID, reason); err != nil {
		p.controlError(err)
		return
	}
	p.ok("Kicked \"" + target.Username + "\"")
}

func (p *Panel) liveNotice() {
	p.println("")
	text := p.ask("Notice text for all users")
	if text == "" {
		p.warn("Cancelled")
		return
	}
	if err := p.control.Notice(text); err != nil {
		p.controlError(err)
		return
	}
	p.ok("Notice sent")
}

func (p *Panel) liveStopBots() {
	if err := p.control.StopBots(""); err != nil {
		p.controlError(err)
		return
	}
	p.ok("Stopped playback on all bots")
}

// controlError explains a failed command, telling apart "the server is not
// running" from a real problem.
func (p *Panel) controlError(err error) {
	if errors.Is(err, control.ErrNotRunning) {
		p.warn("The server is not running — your changes are saved in the database")
		return
	}
	p.warn("Error: " + err.Error())
}

// offerReload is shown after a change that a running server needs to be told
// about, so the operator is not left wondering why nothing happened.
func (p *Panel) offerReload() {
	if _, running := p.control.Running(); !running {
		return
	}
	if p.ask("The server is running — apply now? (y/n)") != "y" {
		return
	}
	if _, err := p.control.Reload(); err != nil {
		p.controlError(err)
		return
	}
	p.printf("  %s\n", green("Applied to the running server"))
}

func humanDuration(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// green marks something that succeeded. It is not called ok, because that name
// is taken by the idiomatic second return value all over this package.
func green(s string) string { return "\033[32m" + s + "\033[0m" }
