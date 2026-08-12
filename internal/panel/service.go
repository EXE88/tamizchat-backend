package panel

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// serviceMenu helps an operator turn a manually started server into one that
// comes back after a reboot.
//
// The panel writes the unit file only when it can; installing a system service
// needs root, and a panel that silently fails at that is worse than one that
// prints the two commands you need.
func (p *Panel) serviceMenu() {
	p.clear()
	p.banner()
	p.printf("  %s\n\n", bold("Run the server permanently (systemd)"))

	if runtime.GOOS != "linux" {
		p.printf("  This system is %s and has no systemd.\n\n", runtime.GOOS)
		p.println("  To run permanently on Windows use Task Scheduler or nssm,")
		p.println("  and start the server with:\n")
		p.printf("    %s run -db %s\n\n", executablePath(), p.store.Path())
		p.pause()
		return
	}

	unit := p.systemdUnit()
	p.println("  Suggested unit file:\n")
	p.println(dim(indent(unit)))

	p.println("  1) Write to /etc/systemd/system/tamizchat.service")
	p.println("  2) Save to a path of your choice")
	p.println("  0) Back\n")

	switch p.ask("Choose") {
	case "1":
		p.writeUnit("/etc/systemd/system/tamizchat.service", unit)
	case "2":
		path := p.ask("File path")
		if path == "" {
			return
		}
		p.writeUnit(path, unit)
	}
}

func (p *Panel) writeUnit(path, unit string) {
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		p.warn("Could not write the file: " + err.Error() +
			"\n  You probably need to run the panel with sudo.")
		return
	}

	p.println("")
	p.printf("  %s\n\n", green("Written: "+path))
	p.println("  Now run these commands:\n")
	p.println("    sudo systemctl daemon-reload")
	p.println("    sudo systemctl enable --now tamizchat")
	p.println("    sudo systemctl status tamizchat\n")
	p.pause()
}

// systemdUnit builds a unit file for this exact installation, with the paths
// already filled in — copying a template and editing it by hand is where
// mistakes come from.
func (p *Panel) systemdUnit() string {
	exe := executablePath()
	db, err := filepath.Abs(p.store.Path())
	if err != nil {
		db = p.store.Path()
	}
	workdir := filepath.Dir(filepath.Dir(db))
	if workdir == "" || workdir == "." {
		workdir = filepath.Dir(db)
	}

	user := os.Getenv("SUDO_USER")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "tamizchat"
	}

	return fmt.Sprintf(`[Unit]
Description=TamizChat server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
WorkingDirectory=%s
ExecStart=%s run -db %s
Restart=on-failure
RestartSec=5s

# The server only ever needs its own data directory.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=read-only
ReadWritePaths=%s

[Install]
WantedBy=multi-user.target
`, user, workdir, exe, db, filepath.Dir(db))
}

func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "tamizchat"
	}
	if abs, err := filepath.Abs(exe); err == nil {
		return abs
	}
	return exe
}

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return strings.Join(lines, "\n") + "\n"
}
