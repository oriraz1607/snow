package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oriraz/grafana-snow-integration/backend/internal/migrate"
)

func main() {
	dir := flag.String("dir", "./migrations", "Directory containing SQL migration files")
	flag.Parse()

	cmd := "up"
	if flag.NArg() > 0 {
		cmd = strings.ToLower(flag.Arg(0))
	}

	dsn := strings.TrimSpace(os.Getenv("MIGRATE_DATABASE_URL"))
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "error: MIGRATE_DATABASE_URL or DATABASE_URL must be set")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := connectWithRetry(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to PostgreSQL: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())

	runner, err := migrate.NewRunner(conn, *dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading migrations: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "up":
		applied, err := runner.Up(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "migration up failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("migrations applied: %d\n", applied)
		if appRole := strings.TrimSpace(os.Getenv("POSTGRES_APP_USER")); appRole != "" {
			if err := migrate.GrantAppRole(ctx, conn, appRole); err != nil {
				fmt.Fprintf(os.Stderr, "grant app role %q failed: %v\n", appRole, err)
				os.Exit(1)
			}
			fmt.Printf("verified least-privilege grants for role: %s\n", appRole)
		}
	case "down":
		ok, err := runner.Down(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "migration down failed: %v\n", err)
			os.Exit(1)
		}
		if ok {
			fmt.Println("rolled back latest migration")
		} else {
			fmt.Println("no applied migrations to roll back")
		}
	case "status":
		statuses, err := runner.Status(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "migration status failed: %v\n", err)
			os.Exit(1)
		}
		for _, s := range statuses {
			state := "pending"
			if s.Applied {
				state = "applied"
			}
			fmt.Printf("%04d_%s: %s\n", s.Version, s.Name, state)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (expected up, down, or status)\n", cmd)
		os.Exit(1)
	}
}

func connectWithRetry(ctx context.Context, dsn string) (*pgx.Conn, error) {
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for {
		connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := pgx.Connect(connCtx, dsn)
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, lastErr
		}
		time.Sleep(1 * time.Second)
	}
}
