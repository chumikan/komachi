package auth

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/perber/wiki/internal/storage/postgres"
	"github.com/perber/wiki/internal/test_utils"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

func TestAuthStorageContract(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var us *UserStore
			var ss *SessionStore
			var ks *APIKeyStore
			var es *EmailTokenStore
			var pg *postgres.Store
			if backend == "postgres" {
				pg, _ = test_utils.PostgresStore(t)
				if err := pg.Migrate(context.Background()); err != nil {
					t.Fatal(err)
				}
				us = NewPostgresUserStore(pg)
				ss = NewPostgresSessionStore(pg)
				ks = NewPostgresAPIKeyStore(pg)
				es = NewPostgresEmailTokenStore(pg)
			} else {
				var err error
				us, err = NewUserStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				ss, err = NewSessionStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				ks, err = NewAPIKeyStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				es, err = NewEmailTokenStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, close := range []func() error{us.Close, ss.Close, ks.Close, es.Close} {
				t.Cleanup(func() {
					if err := close(); err != nil {
						t.Error(err)
					}
				})
			}
			users := NewUserService(us)
			totpService, err := NewTOTPService(testEncryptionKey())
			if err != nil {
				t.Fatal(err)
			}
			manager := NewSessionManager(ss, "contract-test-jwt-secret-at-least-32-bytes", time.Hour, 24*time.Hour)
			auth := NewAuthService(users, manager, totpService)
			// Direct insertion of an already-computed password hash must not rehash it.
			hash, err := bcrypt.GenerateFromPassword([]byte("existing-password"), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			user := &User{ID: "existing-user", Username: "Alice", Email: "Alice@example.com", Password: string(hash), Role: RoleAdmin}
			if err := us.CreateUser(user); err != nil {
				t.Fatal(err)
			}
			stored, err := us.GetUserByID(user.ID)
			if err != nil || stored.Password != string(hash) {
				t.Fatal("credential or ID changed")
			}
			if _, err := auth.Login("Alice", "wrong"); err == nil {
				t.Fatal("accepted wrong password")
			}
			login, err := auth.Login("Alice", "existing-password")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := auth.ValidateToken(login.Token); err != nil {
				t.Fatal(err)
			}
			if _, err := us.GetUserByUsername("alice"); !errors.Is(err, ErrUserNotFound) {
				t.Fatal("username case changed")
			}
			variant := &User{ID: "case-variant", Username: "alice", Email: "alice@example.com", Password: string(hash), Role: RoleViewer}
			if err := us.CreateUser(variant); err != nil {
				t.Fatalf("case-sensitive uniqueness: %v", err)
			}
			duplicate := *variant
			duplicate.ID = "duplicate"
			if err := us.CreateUser(&duplicate); !errors.Is(err, ErrUserAlreadyExists) {
				t.Fatalf("duplicate mapping: %v", err)
			}
			refreshed, err := auth.RefreshToken(login.RefreshToken)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := auth.RefreshToken(login.RefreshToken); err == nil {
				t.Fatal("revoked refresh accepted")
			}
			if err := auth.RevokeRefreshToken(refreshed.RefreshToken); err != nil {
				t.Fatal(err)
			}
			if _, err := auth.RefreshToken(refreshed.RefreshToken); err == nil {
				t.Fatal("logout accepted")
			}
			if err := ss.CreateSession("expired", user.ID, "refresh", time.Now().Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			if active, err := ss.IsActive("expired", user.ID, "refresh", time.Now()); err != nil || active {
				t.Fatal("expired session active")
			}
			if err := ss.CleanupExpiredSessions(); err != nil {
				t.Fatal(err)
			}

			// Both existing implementations permit two refresh requests that have
			// already validated the same old session to complete. Preserve that race.
			concurrentLogin, err := auth.Login("Alice", "existing-password")
			if err != nil {
				t.Fatal(err)
			}
			oldResolve := manager.resolveUser
			oldBind := manager.bindUserResolver
			var ready sync.WaitGroup
			ready.Add(2)
			manager.resolveUser = func(id string) (*User, error) { ready.Done(); ready.Wait(); return oldResolve(id) }
			manager.bindUserResolver = func(tx *sql.Tx) func(string) (*User, error) {
				resolve := oldBind(tx)
				return func(id string) (*User, error) { ready.Done(); ready.Wait(); return resolve(id) }
			}
			refreshResults := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() { _, err := auth.RefreshToken(concurrentLogin.RefreshToken); refreshResults <- err }()
			}
			for i := 0; i < 2; i++ {
				if err := <-refreshResults; err != nil {
					t.Fatalf("concurrent refresh behavior changed: %v", err)
				}
			}
			manager.resolveUser = oldResolve
			manager.bindUserResolver = oldBind
			keys := NewAPIKeyService(ks, auth)
			key, raw, err := keys.CreateAPIKey(CreateAPIKeyParams{Name: "contract", UserID: user.ID, Role: RoleViewer, CreatedBy: user.ID})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := keys.Resolve(raw); err != nil {
				t.Fatal(err)
			}
			principal, err := keys.Resolve(raw)
			if err != nil || principal.Role != RoleViewer {
				t.Fatal("API key widened owner role")
			}
			if err := ks.Revoke(key.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := keys.Resolve(raw); err == nil {
				t.Fatal("revoked API key accepted")
			}
			expired := time.Now().Add(-time.Hour)
			if err := ks.CreateAPIKey(&APIKey{ID: "expired-key", Name: "expired", UserID: user.ID, Prefix: "expired-prefix", KeyHash: hashSecret("expired-secret"), Role: RoleViewer, CreatedBy: user.ID, CreatedAt: time.Now(), ExpiresAt: &expired}); err != nil {
				t.Fatal(err)
			}
			if _, err := keys.Resolve(apiKeyTokenPrefix + "expired-prefix_expired-secret"); err == nil {
				t.Fatal("expired API key authenticated")
			}
			expiredKey, err := ks.GetByID("expired-key")
			if err != nil || expiredKey.IsActive(time.Now()) {
				t.Fatal("expired API key active")
			}
			emailService := NewEmailTokenService(es, users, auth, nil, "")
			invite, err := users.InviteUser("invited", "invited@example.com", RoleViewer)
			if err != nil {
				t.Fatal(err)
			}
			inviteRaw, err := es.Issue(invite.ID, PurposeInvite, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := emailService.ConfirmInvite(inviteRaw, "new-password")
			if err != nil || accepted.MustSetPassword {
				t.Fatalf("invite: %v", err)
			}
			if _, err := emailService.ConfirmInvite(inviteRaw, "second-password"); err == nil {
				t.Fatal("invite reused")
			}
			invitedLogin, err := auth.Login("invited", "new-password")
			if err != nil {
				t.Fatal(err)
			}
			resetRaw, err := es.Issue(invite.ID, PurposePasswordReset, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := emailService.ConfirmPasswordReset(resetRaw, "reset-password"); err != nil {
				t.Fatal(err)
			}
			if _, err := auth.RefreshToken(invitedLogin.RefreshToken); err == nil {
				t.Fatal("reset kept old session")
			}
			if _, err := emailService.ConfirmPasswordReset(resetRaw, "again-password"); err == nil {
				t.Fatal("reset token reused")
			}
			if _, err := auth.Login("invited", "reset-password"); err != nil {
				t.Fatal(err)
			}
			expiredRaw, err := es.Issue(invite.ID, PurposeInvite, -time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := es.Resolve(expiredRaw, PurposeInvite); err == nil {
				t.Fatal("expired email token accepted")
			}
			setup, err := auth.StartTOTPSetup(user.ID, "existing-password")
			if err != nil {
				t.Fatal(err)
			}
			plain, err := totpService.decrypt(setup.EncryptedSecret)
			if err != nil {
				t.Fatal(err)
			}
			code, err := totp.GenerateCode(plain, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			recovery, err := auth.ConfirmTOTPSetup(user.ID, code, "")
			if err != nil {
				t.Fatal(err)
			}
			enabled, err := us.GetUserByID(user.ID)
			if err != nil || !enabled.TOTPEnabled || enabled.TOTPSecretEncrypted != setup.EncryptedSecret {
				t.Fatal("TOTP storage changed")
			}
			challenge, err := auth.Login("Alice", "existing-password")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := auth.CompleteTOTPLogin(challenge.LoginChallengeToken, code); err != nil {
				t.Fatal(err)
			}
			challenge, err = auth.Login("Alice", "existing-password")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := auth.CompleteTOTPLogin(challenge.LoginChallengeToken, recovery[0]); err != nil {
				t.Fatal(err)
			}
			challenge, err = auth.Login("Alice", "existing-password")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := auth.CompleteTOTPLogin(challenge.LoginChallengeToken, recovery[0]); err == nil {
				t.Fatal("recovery code reused")
			}
			if err := auth.DisableTOTP(user.ID, "existing-password", code, ""); err != nil {
				t.Fatal(err)
			}
			// A token CAS must have exactly one winner on either database.
			oneRaw, err := es.Issue(user.ID, PurposeInvite, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			tok, err := es.Resolve(oneRaw, PurposeInvite)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); results <- es.Consume(tok.ID) }()
			}
			wg.Wait()
			close(results)
			wins := 0
			for err := range results {
				if err == nil {
					wins++
				}
			}
			if wins != 1 {
				t.Fatal("email token CAS has multiple winners")
			}
			if pg != nil {
				// An error after token consumption cannot commit a partial password reset.
				raw, err := es.Issue(invite.ID, PurposePasswordReset, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				_, err = pg.DB().Exec(context.Background(), `CREATE FUNCTION fail_password() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$; CREATE TRIGGER fail_password BEFORE UPDATE OF password ON users FOR EACH ROW EXECUTE FUNCTION fail_password()`)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := emailService.ConfirmPasswordReset(raw, "rollback-password"); err == nil {
					t.Fatal("expected reset failure")
				}
				if _, err := es.Resolve(raw, PurposePasswordReset); err != nil {
					t.Fatal("failed reset consumed token")
				}
				if _, err := auth.Login("invited", "reset-password"); err != nil {
					t.Fatal("failed reset changed password")
				}
				if _, err := pg.DB().Exec(context.Background(), `DROP TRIGGER fail_password ON users; DROP FUNCTION fail_password()`); err != nil {
					t.Fatal(err)
				}

				// More concurrent refreshes than pool slots must not require nested
				// connections while a transaction already holds one.
				tokens := make([]string, 16)
				for i := range tokens {
					issued, err := manager.IssueSession(user)
					if err != nil {
						t.Fatal(err)
					}
					tokens[i] = issued.RefreshToken
				}
				refreshedMany := make(chan error, len(tokens))
				for _, token := range tokens {
					go func(token string) { _, err := auth.RefreshToken(token); refreshedMany <- err }(token)
				}
				for range tokens {
					if err := <-refreshedMany; err != nil {
						t.Fatalf("pool-saturated refresh: %v", err)
					}
				}
				// A dead transaction cannot write credentials; the shared pool recovers.
				err = postgres.WithSQLTx(us.db, func(tx *sql.Tx) error {
					var pid int
					if err := tx.QueryRow("SELECT pg_backend_pid()").Scan(&pid); err != nil {
						return err
					}
					if _, err := pg.DB().Exec(context.Background(), "SELECT pg_terminate_backend($1)", pid); err != nil {
						return err
					}
					return us.bind(tx).UpdatePassword(user.ID, "must-not-persist")
				})
				if err == nil {
					t.Fatal("terminated auth transaction succeeded")
				}
				if _, err := auth.Login("Alice", "existing-password"); err != nil {
					t.Fatalf("auth did not recover: %v", err)
				}
				// Two simultaneous demotions must not remove every administrator.
				if err := us.CreateUser(&User{ID: "second-admin", Username: "SecondAdmin", Email: "SecondAdmin@example.com", Password: string(hash), Role: RoleAdmin}); err != nil {
					t.Fatal(err)
				}
				demotions := make(chan error, 2)
				for _, id := range []string{user.ID, "second-admin"} {
					go func(id string) {
						u, err := us.GetUserByID(id)
						if err == nil {
							u.Role = RoleViewer
							err = us.UpdateUser(u)
						}
						demotions <- err
					}(id)
				}
				demoted := 0
				for i := 0; i < 2; i++ {
					err := <-demotions
					if err == nil {
						demoted++
					} else if !errors.Is(err, ErrLastAdminCannotBeDemoted) {
						t.Fatal(err)
					}
				}
				if demoted != 1 {
					t.Fatal("last-admin concurrency invariant broken")
				}
				reopened := NewPostgresUserStore(pg)
				defer reopened.Close()
				u, err := reopened.GetUserByID(user.ID)
				if err != nil || u.Password != string(hash) {
					t.Fatal("reopen lost password")
				}
			}
		})
	}
}
