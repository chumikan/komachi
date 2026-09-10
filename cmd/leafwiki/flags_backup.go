package main

import (
	"time"

	"github.com/urfave/cli/v3"
)

// backupOptions holds the options for snapshots and restore.
type backupOptions struct {
	snapshot             bool
	snapshotInterval     time.Duration
	snapshotRetention    int
	snapshotDir          string
	restoreUploadMaxSize string
}

// Flags declares them. Adding an option here is the whole change: the flag,
// its default, its environment variable and its help text are one literal,
// and --help is generated from it.
func (o *backupOptions) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:        "snapshot",
			Destination: &o.snapshot,
			Category:    catSnapshots,
			Usage:       "full backup snapshots (ZIP including the PostgreSQL database); disable with --snapshot=false",
			Value:       true,
			DefaultText: "true",
			Sources:     envBoolVars("LEAFWIKI_SNAPSHOT"),
		},
		&cli.DurationFlag{
			Name:        "snapshot-interval",
			Destination: &o.snapshotInterval,
			Category:    catSnapshots,
			Usage:       "snapshot interval (e.g. 24h, 6h); 0 = manual-only, no automatic scheduling",
			Value:       24 * time.Hour,
			Sources:     envVars("LEAFWIKI_SNAPSHOT_INTERVAL"),
			DefaultText: "24h",
		},
		&cli.IntFlag{
			Name:        "snapshot-retention",
			Destination: &o.snapshotRetention,
			Category:    catSnapshots,
			Usage:       "number of most recent snapshots to keep; <= 0 = keep all",
			Value:       10,
			Sources:     envVars("LEAFWIKI_SNAPSHOT_RETENTION"),
		},
		&cli.StringFlag{
			Name:        "snapshot-dir",
			Destination: &o.snapshotDir,
			Category:    catSnapshots,
			Usage:       "directory to store snapshot ZIPs in",
			DefaultText: "<data-dir>/snapshots",
			Sources:     envVars("LEAFWIKI_SNAPSHOT_DIR"),
			Config:      trimmed,
		},
		&cli.StringFlag{
			Name:             "restore-upload-max-size",
			Destination:      &o.restoreUploadMaxSize,
			Category:         catSnapshots,
			Usage:            "maximum size of an uploaded backup ZIP to restore from (e.g. 500MiB, 500MB, 524288000)",
			Value:            "500MiB",
			Sources:          envVars("LEAFWIKI_RESTORE_UPLOAD_MAX_SIZE"),
			Validator:        validateByteSizeValue,
			ValidateDefaults: true,
			Config:           trimmed,
		},
	}
}
