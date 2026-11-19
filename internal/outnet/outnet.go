// Package outnet 统一出站 HTTP 连接的两个平台差异点：
//
//  1. DNS：Android 没有 /etc/resolv.conf，Go 的纯 Go 解析器会退回 [::1]:53，
//     查任何域名都变成「连本机 53 端口」→ dial tcp: lookup xxx on [::1]:53: connection refused。
//     所以必须显式指定 DNS 服务器（配置里的 dns_servers）。
//  2. 根证书：Go 在 Android 上读不到系统根证书（SystemCertPool 为空），
//     所有 HTTPS 会死在 x509: certificate signed by unknown authority（见 internal/netca）。
//
// 这两件事以前散落在各处：DNS 只打到了 upstream 的两个 client 和 http.DefaultTransport，
// 根证书压根没人管；结果面板登录、外部签到这些「另起的 http.Client」各缺一半。
// 现在统一从这里造 Transport，谁新建 client 都不会再漏。
package outnet

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/netca"
)

const (
	dialTimeout = 10 * time.Second
	keepAlive   = 30 * time.Second
	// 单台 DNS 服务器建连上限（多台依次尝试）。
	dnsDialTimeout = 5 * time.Second
)

var (
	mu      sync.RWMutex
	dnsList []string
	// dialer 当前生效的拨号器；SetDNSServers 之后整体替换。
	dialer = newOptionalDialer(nil)
)

// SetDNSServers 配置出站解析用的 DNS 服务器（空 = 退回系统解析器）。
// 只影响之后发起的连接；已建立的空闲连接不受影响（那本来就已解析过）。
func SetDNSServers(servers []string) {
	mu.Lock()
	defer mu.Unlock()
	clean := make([]string, 0, len(servers))
	for _, s := range servers {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	dnsList = clean
	dialer = newOptionalDialer(clean)
}

// DNSServers 返回当前生效的 DNS 列表（只读快照，日志/诊断用）。
func DNSServers() []string {
	mu.RLock()
	defer mu.RUnlock()
	return append([]string(nil), dnsList...)
}

// Dialer 返回当前拨号器。调用方拿到的是快照指针：SetDNSServers 会换新对象，
// 所以**不要**在构造期把它存进 Transport 字段里长期持有（那样会回到「配置变了但
// 老 client 还用旧解析器」的老问题），要用 DialContext 这个间接层。
func Dialer() *net.Dialer {
	mu.RLock()
	defer mu.RUnlock()
	return dialer
}

// DialContext 间接调用当前拨号器：每次建连都取最新配置。
// Transport.DialContext 一定用这个函数，而不是 Dialer().DialContext。
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return Dialer().DialContext(ctx, network, addr)
}

// NewTransport 构造一个「DNS + 根证书都已就位」的 Transport。
// 需要自定义超时/并发上限的调用方，拿到后在返回值上改字段即可。
func NewTransport() *http.Transport {
	return &http.Transport{
		// 间接层：配置热更新后仍然生效。
		DialContext:           DialContext,
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0, // 由各 client 的 Timeout 或 ctx 控制
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		// 根证书池显式指定：Android 上系统池为空，不设就是 unknown authority。
		TLSClientConfig: &tls.Config{RootCAs: netca.Pool(), MinVersion: tls.VersionTLS12},
	}
}

// NewTransportWithDNS 是 NewTransport 的便利重载：先设置 DNS 再构造。
// 主要给 cmd/ 下的独立工具（login/credit 等）用，避免它们各自漏配。
func NewTransportWithDNS(servers []string) *http.Transport {
	SetDNSServers(servers)
	return NewTransport()
}

// newOptionalDialer 构造带可选自定义解析器的拨号器。
func newOptionalDialer(servers []string) *net.Dialer {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: keepAlive}
	if len(servers) == 0 {
		return d
	}
	d.Resolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dd := net.Dialer{Timeout: dnsDialTimeout}
			var lastErr error
			for _, s := range servers {
				addr := s
				if _, _, err := net.SplitHostPort(addr); err != nil {
					addr = net.JoinHostPort(s, "53")
				}
				c, err := dd.DialContext(ctx, network, addr)
				if err == nil {
					return c, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = errors.New("dns: 未配置服务器")
			}
			return nil, lastErr
		},
	}
	return d
}
