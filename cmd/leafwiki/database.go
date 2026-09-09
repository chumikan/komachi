package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/perber/wiki/internal/storage/postgres"
	"github.com/urfave/cli/v3"
)

func newDatabaseCommand() *cli.Command {
	config := postgres.DefaultConfig("")
	var timeout time.Duration
	action := func(migrate bool) cli.ActionFunc {
		return func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return errors.New("database commands do not accept positional arguments")
			}
			if timeout <= 0 {
				return errors.New("database timeout must be positive")
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			store, err := postgres.Open(ctx, config)
			if err != nil {
				return err
			}
			defer store.Close()
			if migrate {
				if err := store.Migrate(ctx); err != nil {
					return err
				}
			}
			status, err := store.Status(ctx)
			if err != nil {
				return err
			}
			extension := status.PGroongaVersion
			if extension == "" {
				extension = "not installed"
			}
			out := cmd.Root().Writer
			if _, err := fmt.Fprintf(out, "PostgreSQL: %s\nPGroonga: %s\n", status.PostgreSQLVersion, extension); err != nil {
				return err
			}
			for _, m := range status.Migrations {
				state := "pending"
				if m.AppliedAt != nil {
					state = "applied"
				}
				if _, err := fmt.Fprintf(out, "%04d %s %s\n", m.Version, state, m.Name); err != nil {
					return err
				}
			}
			return nil
		}
	}
	return &cli.Command{
		Name: "database", Usage: "Manage PostgreSQL migrations, legacy import, and complete backups",
		Description: "Use a dedicated PostgreSQL database. Migrations are explicit and transactional; status is read-only. Prefer LEAFWIKI_DATABASE_URL over putting credentials in command arguments.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "database-url", Usage: "PostgreSQL connection URL", Sources: envVars("LEAFWIKI_DATABASE_URL"), Destination: &config.URL, Config: trimmed},
			&cli.Int32Flag{Name: "max-conns", Usage: "Maximum pool connections", Value: config.MaxConns, Destination: &config.MaxConns},
			&cli.Int32Flag{Name: "min-conns", Usage: "Minimum pool connections", Value: config.MinConns, Destination: &config.MinConns},
			&cli.DurationFlag{Name: "connect-timeout", Usage: "Connection establishment timeout", Value: config.ConnectTimeout, Destination: &config.ConnectTimeout},
			&cli.DurationFlag{Name: "max-conn-lifetime", Usage: "Maximum connection lifetime", Value: config.MaxConnLifetime, Destination: &config.MaxConnLifetime},
			&cli.DurationFlag{Name: "max-conn-idle-time", Usage: "Maximum idle connection time", Value: config.MaxConnIdleTime, Destination: &config.MaxConnIdleTime},
			&cli.DurationFlag{Name: "timeout", Usage: "Deadline for the entire database command", Value: 2 * time.Minute, Destination: &timeout},
		},
		Commands: []*cli.Command{
			newLegacyImportCommand(&config, &timeout),
			newPostgresBackupCommand(&config, &timeout),
			{Name: "migrate", Usage: "Apply pending PostgreSQL migrations", Action: action(true)},
			{Name: "status", Usage: "Read PostgreSQL version, extension, and migration status", Action: action(false)},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			return errors.New("choose leafwiki database migrate, status, import-legacy, or backup")
		},
	}
}
