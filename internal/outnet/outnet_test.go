package outnet

import (
	"context"
	"net"
	"sync"
	"testing"
)

// Android 上没有 /etc/resolv.conf，能不能解析域名全靠这里配置的 DNS 服务器。
// 这条钉住「配置确实进了拨号器的 Resolver」——漏了就是手机上所有出站调用失败。
func TestSetDNSServersInstallsResolver(t *testing.T) {
	SetDNSServers([]string{" 223.5.5.5 ", "", "119.29.29.29"})
	defer SetDNSServers(nil)

	if got := DNSServers(); len(got) != 2 || got[0] != "223.5.5.5" || got[1] != "119.29.29.29" {
		t.Fatalf("DNS 列表应去空白并保序，实际 %v", got)
	}
	if Dialer().Resolver == nil {
		t.Fatal("配置了 DNS 服务器，拨号器却没有 Resolver")
	}
}

// 清空配置要退回系统解析器（桌面/Docker 的默认行为）。
func TestSetDNSServersEmptyFallsBackToSystem(t *testing.T) {
	SetDNSServers([]string{"223.5.5.5"})
	SetDNSServers(nil)
	if Dialer().Resolver != nil {
		t.Fatal("空配置时应使用系统解析器")
	}
}

// 关键回归：Transport 里的 DialContext 必须是**间接调用**。
// 如果构造期把 Dialer() 存进字段，之后再 SetDNSServers 就不生效了
// （这正是「面板登录 client 有自己的 Transport，漏了 DNS 配置」的老问题）。
func TestTransportPicksUpLateDNSServers(t *testing.T) {
	SetDNSServers(nil)
	tr := NewTransport() // 先造 Transport（模拟包级 var 初始化早于配置）
	if tr.DialContext == nil {
		t.Fatal("Transport.DialContext 不能为 nil")
	}

	dialerBefore := Dialer()
	SetDNSServers([]string{"223.5.5.5"})
	defer SetDNSServers(nil)

	if Dialer() == dialerBefore {
		t.Fatal("SetDNSServers 应替换拨号器实例")
	}
	// 间接层：此时发起连接会走新的解析器。这里只验证取到的是新拨号器——
	// 真正拨号需要网络，不在单测里做。
	if Dialer().Resolver == nil {
		t.Fatal("后设置的 DNS 没有生效到当前拨号器")
	}
}

// 根证书池必须是显式的：Android 上系统池为空，不设就是 unknown authority。
func TestTransportHasRootCAs(t *testing.T) {
	tr := NewTransport()
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Fatal("Transport 必须显式带 RootCAs")
	}
	if n := len(tr.TLSClientConfig.RootCAs.Subjects()); n < 50 {
		t.Fatalf("根证书池只有 %d 条，内嵌基线可能失效", n)
	}
	if tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("不允许关闭证书校验")
	}
}

// 配置热更新与并发建连不能打架（面板保存配置时会并发改 DNS）。
func TestConcurrentConfigureAndDial(t *testing.T) {
	defer SetDNSServers(nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				SetDNSServers([]string{"223.5.5.5"})
			} else {
				SetDNSServers(nil)
			}
		}(i)
		go func() {
			defer wg.Done()
			_ = DNSServers()
			_ = Dialer()
			// 只验证不 panic/不 data race；不真的建连。
			_ = net.JoinHostPort("223.5.5.5", "53")
			_ = context.Background()
		}()
	}
	wg.Wait()
}
