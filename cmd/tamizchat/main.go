// Command tamizchat is both the server and its administration panel.
//
//	tamizchat          # open the interactive admin panel
//	tamizchat run      # run the server in the foreground
//	tamizchat version  # print build info
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"tamizchat/internal/app"
	"tamizchat/internal/logging"
	"tamizchat/internal/panel"
	"tamizchat/internal/version"
)

const defaultDBPath = "data/tamizchat.db"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "tamizchat: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("tamizchat", flag.ContinueOnError)
	dbPath := fs.String("db", envOr("TAMIZCHAT_DB", defaultDBPath), "مسیر فایل دیتابیس SQLite")
	logLevel := fs.String("log", "info", "سطح لاگ اولیه: debug|info|warn|error")
	fs.Usage = usage

	cmd := "panel"
	args := os.Args[1:]
	if len(args) > 0 && !isFlag(args[0]) {
		cmd, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	logging.Setup(*logLevel)
	ctx := context.Background()

	switch cmd {
	case "run", "serve":
		return app.Run(ctx, app.Options{DBPath: *dbPath})
	case "panel", "admin":
		return panel.Run(ctx, *dbPath)
	case "version":
		fmt.Printf("tamizchat %s (%s)\n", version.Version, version.Commit)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("دستور ناشناخته: %s", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `TamizChat backend

استفاده:
  tamizchat [دستور] [پرچم‌ها]

دستورها:
  panel     پنل مدیریت تعاملی (پیش‌فرض)
  run       اجرای سرور
  version   نمایش نسخه

پرچم‌ها:
  -db     مسیر فایل دیتابیس (پیش‌فرض data/tamizchat.db، یا TAMIZCHAT_DB)
  -log    سطح لاگ اولیه: debug|info|warn|error
`)
}

func isFlag(s string) bool { return len(s) > 0 && s[0] == '-' }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
