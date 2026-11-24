package fleet

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clawbench-fleet/internal/domain"
)

// The platform ships one self-signed certificate to every instance instead of
// letting each tenant make its own. That keeps the install simple: employees
// accept one warning per server, not one per port.

const (
	fleetCertFile = "cert.pem"
	fleetKeyFile  = "key.pem"
)

// CertDir is where the platform keeps the certificate it hands out. It lives
// under the data directory so backups pick it up.
func (s *Service) CertDir() string {
	if dir := s.Settings().TLSCertDir; dir != "" {
		return dir
	}
	return filepath.Join(s.Cfg.DataDir, "certs")
}

func (s *Service) certPath() string { return filepath.Join(s.CertDir(), fleetCertFile) }
func (s *Service) keyPath() string  { return filepath.Join(s.CertDir(), fleetKeyFile) }

// tenantCertDir is the per-tenant copy. Each tenant gets its own directory so
// the files can be owned by that tenant without exposing anything else.
func tenantCertDir(t *domain.Tenant) string {
	return filepath.Join(t.DataDir, "config", "certs")
}

// EnsureCertificate makes sure the platform certificate exists. It generates a
// self-signed one on first use and reuses it afterwards, so the warning the
// employees click through stays the same until the operator replaces the pair.
// It returns the certificate path, or "" when TLS is turned off in settings.
func (s *Service) EnsureCertificate() (string, error) {
	if !s.Settings().TLSEnabled {
		return "", nil
	}
	dir := s.CertDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建证书目录 %s: %w", dir, err)
	}
	certPath, keyPath := s.certPath(), s.keyPath()
	if fileExists(certPath) && fileExists(keyPath) {
		return certPath, nil
	}
	hosts := s.certHosts()
	if err := generateSelfSigned(certPath, keyPath, hosts); err != nil {
		return "", err
	}
	return certPath, nil
}

// certHosts collects every name the certificate should be valid for: the
// configured public host, the hosts the server actually answers on, and
// loopback so local health checks keep working.
func (s *Service) certHosts() []string {
	hosts := []string{"localhost", "127.0.0.1"}
	if st := s.Settings(); st.PublicHost != "" {
		hosts = append(hosts, st.PublicHost)
	}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			hosts = append(hosts, ipNet.IP.String())
		}
	}
	return hosts
}

// generateSelfSigned writes a certificate/key pair valid for hosts.
func generateSelfSigned(certPath, keyPath string, hosts []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("生成密钥: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("生成证书序列号: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: platformName, Organization: []string{"Clawbench"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, strings.ToLower(h))
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("签发证书: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("编码私钥: %w", err)
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	return writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	block := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, block, mode); err != nil {
		return fmt.Errorf("写入 %s: %w", path, err)
	}
	return nil
}

// distributeCert copies the platform certificate into a tenant's own config
// directory, owned by that tenant. Clawbench only needs cert.pem and key.pem
// side by side under cert_dir.
func (s *Service) distributeCert(t *domain.Tenant) error {
	if !s.Settings().TLSEnabled {
		return s.removeTenantCert(t)
	}
	certSrc, err := s.EnsureCertificate()
	if err != nil || certSrc == "" {
		return err
	}
	dir := tenantCertDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建实例证书目录 %s: %w", dir, err)
	}
	if err := copyFile(certSrc, filepath.Join(dir, fleetCertFile), 0o644); err != nil {
		return err
	}
	if err := copyFile(s.keyPath(), filepath.Join(dir, fleetKeyFile), 0o600); err != nil {
		return err
	}
	return s.ownCertDir(t, dir)
}

func (s *Service) removeTenantCert(t *domain.Tenant) error {
	dir := tenantCertDir(t)
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("移除实例证书目录 %s: %w", dir, err)
	}
	return nil
}

func (s *Service) ownCertDir(t *domain.Tenant, dir string) error {
	// Only the real backend has real uids to hand the files to.
	if s.Sys.Mode() != "systemd" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.Sys.Chown(ctx, dir, t.UID, t.GID)
	for _, name := range []string{fleetCertFile, fleetKeyFile} {
		_ = s.Sys.Chown(ctx, filepath.Join(dir, name), t.UID, t.GID)
	}
	// config/ was created root-owned before the data directory was handed
	// over; give it back so later writes by the tenant work too.
	_ = s.ownTenantDataDir(t)
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CertInfo is what the settings page shows about the shared certificate.
type CertInfo struct {
	Enabled    bool     `json:"enabled"`
	Dir        string   `json:"dir"`
	Exists     bool     `json:"exists"`
	SelfSigned bool     `json:"self_signed"`
	Subject    string   `json:"subject"`
	DNSNames   []string `json:"dns_names"`
	IPs        []string `json:"ips"`
	NotBefore  string   `json:"not_before"`
	NotAfter   string   `json:"not_after"`
}

// CertInfo reports the current certificate so an operator can see when it
// expires and which addresses it covers.
func (s *Service) CertInfo() CertInfo {
	info := CertInfo{Enabled: s.Settings().TLSEnabled, Dir: s.CertDir()}
	data, err := os.ReadFile(s.certPath())
	if err != nil {
		return info
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return info
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return info
	}
	info.Exists = true
	info.SelfSigned = cert.CheckSignatureFrom(cert) == nil
	info.Subject = cert.Subject.CommonName
	info.DNSNames = cert.DNSNames
	for _, ip := range cert.IPAddresses {
		info.IPs = append(info.IPs, ip.String())
	}
	info.NotBefore = cert.NotBefore.Format(time.RFC3339)
	info.NotAfter = cert.NotAfter.Format(time.RFC3339)
	return info
}

// RegenerateCertificate throws the current pair away and makes a new one. It is
// the escape hatch for an expired certificate or a server whose addresses
// changed since the pair was created.
func (s *Service) RegenerateCertificate() (string, error) {
	if err := os.RemoveAll(s.CertDir()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("清理旧证书 %s: %w", s.CertDir(), err)
	}
	return s.EnsureCertificate()
}
