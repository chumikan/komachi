package tools

import (
	"github.com/perber/wiki/internal/storage/postgres"
	"log/slog"

	"github.com/perber/wiki/internal/core/auth"
)

func ResetAdminPassword(storageDir, username, email string) (*auth.User, error) {
	store, err := auth.NewUserStore(storageDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Default().Error("could not close store", "error", err)
		}
	}()

	userService := auth.NewUserService(store)
	return userService.ResetAdminUserPassword(username, email)
}

func ResetPostgresAdminPassword(pg *postgres.Store, username, email string) (*auth.User, error) {
	store := auth.NewPostgresUserStore(pg)
	defer store.Close()
	return auth.NewUserService(store).ResetAdminUserPassword(username, email)
}
