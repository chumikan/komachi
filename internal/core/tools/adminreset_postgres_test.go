package tools

import (
	"context"
	"github.com/perber/wiki/internal/core/auth"
	"github.com/perber/wiki/internal/test_utils"
	"testing"
)

func TestResetPostgresAdminPassword(t *testing.T) {
	pg, _ := test_utils.PostgresStore(t)
	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	user, err := ResetPostgresAdminPassword(pg, "admin", "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewPostgresUserStore(pg)
	defer store.Close()
	if _, err := auth.NewUserService(store).DoesIDAndPasswordMatch(user.ID, user.Password); err != nil {
		t.Fatal("reset password does not authenticate")
	}
	again, err := ResetPostgresAdminPassword(pg, "ignored", "ignored@example.com")
	if err != nil || again.ID != user.ID {
		t.Fatal("reset changed existing admin identity")
	}
}
