package webui

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TLSMode 是面板的 HTTPS 模式。
type TLSMode string

const (
	TLSOff    TLSMode = "off"
	TLSAuto   TLSMode = "auto"   // 自签证书
	TLSManual TLSMode = "manual" // 导入证书
)

type tlsState struct {
	Mode     TLSMode `json:"mode"`
	CertFile string  `json:"cert_file"`
	KeyFile  string  `json:"key_file"`
}

// TLManager 负责证书的生成、加载与持久化。
type TLManager struct {
	mu        sync.Mutex
	dir       string
	statePath string
	state     tlsState
	cert      tls.Certificate
	loaded    bool
}

// NewTLManager 创建管理器；目录一般为 /etc/sipvoice/tls。
func NewTLManager(dir string) *TLManager {
	_ = os.MkdirAll(dir, 0o700)
	m := &TLManager{dir: dir, statePath: filepath.Join(dir, "state.json")}
	if raw, err := os.ReadFile(m.statePath); err == nil {
		_ = json.Unmarshal(raw, &m.state)
	}
	if m.state.Mode == "" {
		m.state.Mode = TLSAuto
	}
	m.state.CertFile = filepath.Join(dir, "cert.pem")
	m.state.KeyFile = filepath.Join(dir, "key.pem")
	return m
}

// Mode 返回当前模式。
func (m *TLManager) Mode() TLSMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Mode
}

// Certificate 返回可用证书；auto 模式下缺证书时自动生成。
func (m *TLManager) Certificate() (tls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded {
		return m.cert, nil
	}
	if m.state.Mode == TLSOff {
		return tls.Certificate{}, fmt.Errorf("HTTPS 已关闭")
	}
	if _, err := os.Stat(m.state.CertFile); err != nil {
		if m.state.Mode != TLSAuto {
			return tls.Certificate{}, fmt.Errorf("证书文件不存在: %v", err)
		}
		if err := m.generateLocked(); err != nil {
			return tls.Certificate{}, err
		}
	}
	cert, err := tls.LoadX509KeyPair(m.state.CertFile, m.state.KeyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	m.cert = cert
	m.loaded = true
	return cert, nil
}

// SetMode 切换模式并持久化（证书在下次 Certificate 调用时加载）。
func (m *TLManager) SetMode(mode TLSMode) error {
	switch mode {
	case TLSOff, TLSAuto, TLSManual:
	default:
		return fmt.Errorf("未知模式: %s", mode)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Mode = mode
	m.loaded = false
	return m.saveLocked()
}

// SetManual 保存导入的证书与私钥。
func (m *TLManager) SetManual(certPEM, keyPEM []byte) error {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return fmt.Errorf("证书与私钥不匹配: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.WriteFile(m.state.CertFile, certPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(m.state.KeyFile, keyPEM, 0o600); err != nil {
		return err
	}
	m.state.Mode = TLSManual
	m.loaded = false
	return m.saveLocked()
}

// Info 返回证书信息用于面板展示。
func (m *TLManager) Info() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	info := map[string]any{"mode": string(m.state.Mode)}
	cert, err := tls.LoadX509KeyPair(m.state.CertFile, m.state.KeyFile)
	if err != nil || len(cert.Certificate) == 0 {
		info["ok"] = false
		return info
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		info["ok"] = false
		return info
	}
	info["ok"] = true
	info["subject"] = leaf.Subject.CommonName
	info["dns"] = leaf.DNSNames
	var ips []string
	for _, ip := range leaf.IPAddresses {
		ips = append(ips, ip.String())
	}
	info["ips"] = ips
	info["not_after"] = leaf.NotAfter.Format("2006-01-02")
	info["self_signed"] = leaf.Issuer.String() == leaf.Subject.String()
	info["expired"] = time.Now().After(leaf.NotAfter)
	return info
}

func (m *TLManager) saveLocked() error {
	raw, _ := json.MarshalIndent(m.state, "", "  ")
	return os.WriteFile(m.statePath, raw, 0o600)
}

// generateLocked 生成自签证书（ECDSA P-256，2 年有效）。
func (m *TLManager) generateLocked() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	dns := []string{"localhost", "sipvoice.local"}
	if host != "" {
		dns = append(dns, host)
	}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				ips = append(ips, ipnet.IP)
			}
		}
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "SIPVoice Control Panel", Organization: []string{"SIPVoice"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(m.state.CertFile, certPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(m.state.KeyFile, keyPEM, 0o600)
}
