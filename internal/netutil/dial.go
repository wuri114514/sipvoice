// Package netutil 提供带 IPv4 优先策略的拨号器。
//
// 家庭宽带下虚机的 IPv6 出网经常被黑洞，而 DNS 往往先返回 AAAA，
// 默认的拨号策略会让每次新连接白等数百毫秒甚至数秒。这里先解析、
// 把 IPv4 排前面逐个尝试，再退回 IPv6。
package netutil

import (
	"context"
	"crypto/tls"
	"net"
	"time"
)

// DialContext 优先 IPv4，失败再试 IPv6。
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	if ip := net.ParseIP(host); ip != nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	var lastErr error
	for _, ip := range ordered(ips) {
		d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// DialTLSContext 在 DialContext 的基础上叠加 TLS 握手。
func DialTLSContext(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
	raw, err := DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(addr)
	c := cfg
	if c == nil {
		c = &tls.Config{}
	}
	if c.ServerName == "" {
		c = c.Clone()
		c.ServerName = host
	}
	tc := tls.Client(raw, c)
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := tc.HandshakeContext(tctx); err != nil {
		raw.Close()
		return nil, err
	}
	return tc, nil
}

func ordered(ips []net.IPAddr) []net.IPAddr {
	out := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			out = append(out, ip)
		}
	}
	for _, ip := range ips {
		if ip.IP.To4() == nil {
			out = append(out, ip)
		}
	}
	return out
}
