package fleet

import (
	"errors"
	"testing"
)

func TestLoginLogoutAndAuthenticate(t *testing.T) {
	svc := newService(t)
	initial, created, err := BootstrapAdmin(svc.Cfg.InitialPasswordPath(), svc.Store, "seed-password-1")
	if err != nil || !created || initial != "seed-password-1" {
		t.Fatalf("bootstrap = %q %v %v", initial, created, err)
	}
	// Bootstrapping twice is a no-op.
	if _, created, err := BootstrapAdmin("", svc.Store, "other"); err != nil || created {
		t.Fatalf("second bootstrap = %v %v", created, err)
	}
	if n, err := svc.AdminCount(); err != nil || n != 1 {
		t.Fatalf("admin count = %d %v", n, err)
	}

	if _, _, checkErr := svc.Login("admin", "wrong", "", ""); checkErr == nil {
		t.Fatal("wrong password must fail")
	}
	if _, _, checkErr := svc.Login("ghost", "seed-password-1", "", ""); checkErr == nil {
		t.Fatal("unknown user must fail")
	}
	admin, token, err := svc.Login("admin", "seed-password-1", "10.0.0.1", "ua")
	if err != nil || token == "" || admin.Username != "admin" {
		t.Fatalf("login = %+v %q %v", admin, token, err)
	}
	got, err := svc.Authenticate(token)
	if err != nil || got.ID != admin.ID {
		t.Fatalf("authenticate = %+v %v", got, err)
	}
	if _, checkErr := svc.Authenticate(""); checkErr == nil {
		t.Fatal("empty token must fail")
	}
	if _, checkErr := svc.Authenticate("not-a-session"); checkErr == nil {
		t.Fatal("unknown token must fail")
	}

	if err := svc.ChangeAdminPassword(admin.ID, "wrong", "new-password-2"); err == nil {
		t.Fatal("wrong current password must fail")
	}
	if err := svc.ChangeAdminPassword(admin.ID, "seed-password-1", "short"); err == nil {
		t.Fatal("weak password must fail")
	}
	if err := svc.ChangeAdminPassword(admin.ID, "seed-password-1", "new-password-2"); err != nil {
		t.Fatal(err)
	}
	// Changing the password drops every existing session.
	if _, checkErr := svc.Authenticate(token); checkErr == nil {
		t.Fatal("old session should be gone")
	}
	if _, _, err := svc.Login("admin", "new-password-2", "", ""); err != nil {
		t.Fatal(err)
	}
	svc.Logout(token)
	svc.Logout("")
}

func TestAuthenticateExpiredSession(t *testing.T) {
	svc := newService(t)
	if _, _, err := BootstrapAdmin("", svc.Store, "seed-password-1"); err != nil {
		t.Fatal(err)
	}
	admin, token, err := svc.Login("admin", "seed-password-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Backdate the session so it looks expired.
	sess, err := svc.Store.GetSession(hashToken(token))
	if err != nil {
		t.Fatal(err)
	}
	sess.ExpiresAt = "2000-01-01T00:00:00Z"
	if err := svc.Store.DeleteSession(sess.TokenHash); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.CreateSession(sess); err != nil {
		t.Fatal(err)
	}
	if _, checkErr := svc.Authenticate(token); checkErr == nil {
		t.Fatal("expired session must be rejected")
	}
	_ = admin
}

func TestBootstrapWritesInitialPasswordFile(t *testing.T) {
	svc := newService(t)
	password, created, err := BootstrapAdmin(svc.Cfg.InitialPasswordPath(), svc.Store, "")
	if err != nil || !created || len(password) != 16 {
		t.Fatalf("bootstrap = %q %v %v", password, created, err)
	}
	if _, _, err := svc.Login("admin", password, "", ""); err != nil {
		t.Fatalf("generated password should work: %v", err)
	}
}

func TestAuthErrorsAreOpaque(t *testing.T) {
	svc := newService(t)
	if _, _, err := BootstrapAdmin("", svc.Store, "seed-password-1"); err != nil {
		t.Fatal(err)
	}
	_, _, wrongPassword := svc.Login("admin", "nope-nope", "", "")
	_, _, unknownUser := svc.Login("nobody", "nope-nope", "", "")
	if wrongPassword == nil || unknownUser == nil || wrongPassword.Error() != unknownUser.Error() {
		t.Fatalf("error messages must not reveal which part was wrong: %v / %v", wrongPassword, unknownUser)
	}
}

func TestErrTenantNotFoundWrapsStoreError(t *testing.T) {
	svc := newService(t)
	if _, err := svc.GetTenant(999); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("missing tenant = %v", err)
	}
	if _, checkErr := svc.TenantDetail(999); checkErr == nil {
		t.Fatal("detail for a missing tenant must fail")
	}
}
