package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/perber/wiki/internal/storage/postgres"
	"github.com/perber/wiki/internal/transfer"
	"github.com/urfave/cli/v3"
)

func newLegacyImportCommand(config *postgres.Config, timeout *time.Duration) *cli.Command {
	var source, target string
	var dry bool
	return &cli.Command{Name: "import-legacy", Usage: "Import a stopped legacy workspace into an empty migrated PostgreSQL database and empty target directory", Description: "Source is read-only. Stop both source and target applications. Use --dry-run first. Existing data is never merged. Commit uncertainty leaves a transfer journal and prevents server startup.", Flags: []cli.Flag{
		&cli.StringFlag{Name: "source", Required: true, Destination: &source}, &cli.StringFlag{Name: "target", Required: true, Destination: &target}, &cli.BoolFlag{Name: "dry-run", Destination: &dry},
	}, Action: func(ctx context.Context, cmd *cli.Command) error {
		if cmd.Args().Present() {
			return errors.New("unexpected positional arguments")
		}
		if *timeout <= 0 {
			return errors.New("database timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		pg, err := postgres.Open(ctx, *config)
		if err != nil {
			return err
		}
		defer pg.Close()
		report, importErr := transfer.ImportLegacy(ctx, pg, source, target, dry)
		if err := json.NewEncoder(cmd.Root().Writer).Encode(report); err != nil {
			return err
		}
		return importErr
	}}
}

func newPostgresBackupCommand(config *postgres.Config, timeout *time.Duration) *cli.Command {
	var dir, output string
	return &cli.Command{Name: "backup", Usage: "Create a complete PostgreSQL backup while the application is stopped", Description: transfer.TrustNotice + " Includes secret-bearing database data and filesystem assets. Use Full Backup in the Web UI for a running instance.", Flags: []cli.Flag{
		&cli.StringFlag{Name: "data-dir", Required: true, Destination: &dir}, &cli.StringFlag{Name: "output", Required: true, Destination: &output},
	}, Action: func(ctx context.Context, cmd *cli.Command) error {
		if cmd.Args().Present() {
			return errors.New("unexpected positional arguments")
		}
		if *timeout <= 0 {
			return errors.New("database timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		pg, err := postgres.Open(ctx, *config)
		if err != nil {
			return err
		}
		defer pg.Close()
		if err := transfer.CreateBackup(ctx, pg, config.URL, dir, output, "snapshot-"+time.Now().UTC().Format("20060102-150405"), Version, transfer.PGTools{}); err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.Root().Writer, "Complete PostgreSQL backup created.")
		return err
	}}
}
