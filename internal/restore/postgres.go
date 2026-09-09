package restore

import (
	"context"
	"errors"

	coreshared "github.com/perber/wiki/internal/core/shared"
	"github.com/perber/wiki/internal/transfer"
)

func (m *Manager) runPostgres(path string, limits coreshared.ExtractionLimits) {
	ctx := context.Background()
	m.job.SetPhase(PhaseValidating)
	if m.cfg.WriteGate == nil {
		m.job.Finish(errors.New("PostgreSQL restore requires a write gate"))
		return
	}
	release, err := m.cfg.WriteGate.Freeze(ctx)
	if err != nil {
		m.job.Finish(err)
		return
	}
	m.job.SetPhase(PhaseSwapping)
	maxBytes := int64(0)
	if limits != coreshared.UnrestrictedExtractionLimits {
		maxBytes = limits.MaxTotalBytes
	}
	err = transfer.RestoreBackup(ctx, m.cfg.Database, m.cfg.DatabaseURL, m.cfg.DataDir, path, m.cfg.PGTools, transfer.RestoreOptions{
		Replace: true, MaxBytes: maxBytes, AfterRestore: func() error {
			// Existing live restore logs everyone out; offline restore preserves the
			// backed-up session rows. Neither import nor backup rehashes credentials.
			m.job.SetPhase(PhaseInvalidatingSessions)
			if m.cfg.AuthService != nil {
				if err := m.cfg.AuthService.InvalidateAllSessions(); err != nil {
					return err
				}
			}
			m.job.SetPhase(PhaseReloadingBranding)
			if m.cfg.BrandingService != nil {
				if err := m.cfg.BrandingService.Reload(); err != nil {
					return err
				}
			}
			if m.cfg.PublicAccess != nil {
				if err := m.cfg.PublicAccess.Reload(); err != nil {
					return err
				}
			}
			if m.cfg.UserResolver != nil {
				if err := m.cfg.UserResolver.Reload(); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil && transfer.CheckPending(m.cfg.DataDir) != nil {
		m.job.FinishNeedsIntervention(err)
		return
	}
	release()
	if err == nil && m.cfg.TriggerResync != nil {
		m.cfg.TriggerResync()
	}
	m.job.Finish(err)
}
