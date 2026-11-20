package fleet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"

	"golang.org/x/crypto/bcrypt"
)

const SessionCookie = "fleet_session"
const sessionTTL = 7 * 24 * time.Hour

// BootstrapAdmin creates the single admin account on a fresh install and
// writes the generated password to a root-only file. It is idempotent.
// fixedPassword is only used by --dev so automated checks have a known login.
func BootstrapAdmin(cfgInitialPasswordPath string, st *store.Store, fixedPassword string) (string, bool, error) {
	n, err := st.CountAdmins()
	if err != nil {
		return "", false, err
	}
	if n > 0 {
		return "", false, nil
	}
	password := fixedPassword
	if password == "" {
		password, err = GeneratePassword(16)
		if err != nil {
			return "", false, err
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", false, err
	}
	if _, err := st.CreateAdmin("admin", string(hash)); err != nil {
		return "", false, err
	}
	if cfgInitialPasswordPath != "" {
		_ = os.MkdirAll(filepath.Dir(cfgInitialPasswordPath), 0o700)
		if err := os.WriteFile(cfgInitialPasswordPath, []byte(password+"\n"), 0o600); err != nil {
			return password, true, err
		}
	}
	return password, true, nil
}

func ValidateAdminPassword(password string) error {
	n := utf8.RuneCountInString(password)
	if n < 8 || n > 32 {
		return errors.New("密码长度必须是 8-32 位")
	}
	hasLetter, hasDigit := false, false
	for _, r := range password {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
		}
		if r >= '0' && r <= '9' {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("密码必须同时包含字母和数字")
	}
	return nil
}

func (s *Service) Login(username, password, ip, ua string) (*domain.Admin, string, error) {
	admin, err := s.Store.GetAdminByUsername(username)
	if err != nil {
		return nil, "", errors.New("账号或密码不正确")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password)); err != nil {
		return nil, "", errors.New("账号或密码不正确")
	}
	token, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	nowT := time.Now()
	sess := &domain.Session{
		TokenHash: hashToken(token),
		AdminID:   admin.ID,
		CreatedAt: nowT.Format(time.RFC3339),
		ExpiresAt: nowT.Add(sessionTTL).Format(time.RFC3339),
		IP:        ip,
		UserAgent: ua,
	}
	if err := s.Store.CreateSession(sess); err != nil {
		return nil, "", err
	}
	_ = s.Store.TouchAdminLogin(admin.ID)
	return admin, token, nil
}

func (s *Service) Authenticate(token string) (*domain.Admin, error) {
	if token == "" {
		return nil, errors.New("未登录")
	}
	sess, err := s.Store.GetSession(hashToken(token))
	if err != nil {
		return nil, errors.New("会话已失效，请重新登录")
	}
	expires, err := time.Parse(time.RFC3339, sess.ExpiresAt)
	if err != nil || time.Now().After(expires) {
		_ = s.Store.DeleteSession(sess.TokenHash)
		return nil, errors.New("会话已过期，请重新登录")
	}
	admin, err := s.Store.GetAdminByID(sess.AdminID)
	if err != nil {
		return nil, errors.New("账号不存在")
	}
	return admin, nil
}

func (s *Service) Logout(token string) {
	if token != "" {
		_ = s.Store.DeleteSession(hashToken(token))
	}
}

func (s *Service) ChangeAdminPassword(adminID int64, current, next string) error {
	admin, err := s.Store.GetAdminByID(adminID)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(current)); err != nil {
		return errors.New("当前密码不正确")
	}
	if err := ValidateAdminPassword(next); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.Store.UpdateAdminPassword(adminID, string(hash)); err != nil {
		return err
	}
	// Every other session must be re-authenticated after a password change.
	_ = s.Store.DeleteSessionsForAdmin(adminID)
	return nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// SessionMaxAgeSeconds is exposed to the HTTP layer for the cookie Max-Age.
func SessionMaxAgeSeconds() int { return int(sessionTTL.Seconds()) }

func (s *Service) AdminCount() (int, error) { return s.Store.CountAdmins() }
