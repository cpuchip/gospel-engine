// Subcommand: bootstrap-token
//
// Usage (inside the running container):
//   /app/gospel-engine bootstrap-token --name "service" [--user michael] [--rate-limit 600]
//
// Connects to the same DB the server uses, mints a new bearer token, and
// prints the raw secret to stdout exactly once. Safe to run multiple times
// (each call creates a new independent token).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cpuchip/gospel-engine/internal/config"
	"github.com/cpuchip/gospel-engine/internal/db"
)

func init() {
	// Hook into program startup before main() decides what to do.
	if len(os.Args) >= 2 && os.Args[1] == "bootstrap-token" {
		// Strip the subcommand from argv so flag parsing only sees its own flags.
		args := append([]string{os.Args[0]}, os.Args[2:]...)
		os.Args = args
		if err := runBootstrapToken(); err != nil {
			fmt.Fprintln(os.Stderr, "bootstrap-token failed:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func runBootstrapToken() error {
	fs := flag.NewFlagSet("bootstrap-token", flag.ExitOnError)
	name := fs.String("name", "service", "Human-readable token name")
	user := fs.String("user", "", "Optional external user identifier")
	rateLimit := fs.Int("rate-limit", 600, "Requests per minute")
	expiresInDays := fs.Int("expires-in-days", 0, "Token lifetime in days (0 = no expiry)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("--name is required")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer database.Close()

	var exp *time.Time
	if *expiresInDays > 0 {
		t := time.Now().UTC().Add(time.Duration(*expiresInDays) * 24 * time.Hour)
		exp = &t
	}

	tok, raw, err := database.CreateAPIToken(ctx, *user, *name, exp, *rateLimit)
	if err != nil {
		return fmt.Errorf("create token: %w", err)
	}

	fmt.Printf("OK — token created\n")
	fmt.Printf("  id          : %d\n", tok.ID)
	fmt.Printf("  name        : %s\n", tok.Name)
	fmt.Printf("  prefix      : %s\n", tok.Prefix)
	fmt.Printf("  rate_limit  : %d/min\n", tok.RateLimit)
	if tok.ExpiresAt != nil {
		fmt.Printf("  expires_at  : %s\n", tok.ExpiresAt.Format(time.RFC3339))
	} else {
		fmt.Printf("  expires_at  : (never)\n")
	}
	fmt.Printf("\nRAW TOKEN (shown once — store it now):\n%s\n", raw)
	return nil
}
