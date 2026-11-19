// Package netca 提供出站 HTTPS 用的根证书池。
//
// 为什么需要它：Go 在 Android 上**读不到任何系统根证书**。
// Go 的 crypto/x509 在 linux 下只认 /etc/ssl/certs/ca-certificates.crt 这类
// 发行版路径，而 Android 的信任库在 /system/etc/security/cacerts（散列文件名），
// 两个世界不重叠 —— 于是 x509.SystemCertPool() 返回一个**空池**，
// 所有 HTTPS 出站请求都会失败在 "x509: certificate signed by unknown authority"。
// 表现就是：网关在手机上连上游都连不上，连账号都加不了。
//
// 做法：
//  1. 内嵌一份 Mozilla CA 根证书（certs.pem，约 180KB）作为基线；
//  2. 运行环境若还有系统根证书（桌面 / Docker），一并合并——不覆盖用户的证书配置；
//  3. Android 上额外扫描 /system/etc/security/cacerts 与 conscrypt APEX 目录，
//     把厂商预装的根也吃进来（这些目录是只读的系统路径，普通应用可读）。
//
// 不使用 InsecureSkipVerify：证书校验必须照常进行。
package netca

import (
	"crypto/x509"
	_ "embed"
	"log"
	"os"
	"path/filepath"
	"sync"
)

//go:embed certs.pem
var embeddedPEM []byte

// androidCertDirs Android 存放系统根证书的目录（散列文件名，内容就是 PEM/DER）。
// 只读系统路径，普通应用权限即可读取。
var androidCertDirs = []string{
	"/system/etc/security/cacerts",
	"/apex/com.android.conscrypt/cacerts",
}

var (
	once sync.Once
	pool *x509.CertPool
)

// Pool 返回出站 HTTPS 使用的根证书池（进程内构造一次后复用）。
//
// 顺序保证「宁可多信一个基线根，也不要空池」：即使系统池为空，内嵌基线也会生效，
// 这正是 Android 上的关键路径。
func Pool() *x509.CertPool {
	once.Do(func() {
		base, err := x509.SystemCertPool()
		if base == nil || err != nil {
			base = x509.NewCertPool()
		}
		before := len(base.Subjects())

		// 内嵌基线：已存在同名根证书时重复添加无副作用（池按 DER 去重）。
		if !base.AppendCertsFromPEM(embeddedPEM) && before == 0 {
			log.Printf("[netca] 警告: 内嵌根证书解析失败，出站 HTTPS 可能全部失败")
		}

		// Android：把系统信任库里的根也并进来（厂商根、企业根）。
		added := 0
		for _, dir := range androidCertDirs {
			n, err := appendDir(base, dir)
			if err != nil {
				continue
			}
			added += n
		}

		pool = base
		log.Printf("[netca] 根证书池就绪: 系统 %d 条 + Android 目录 %d 个文件（合计 %d 条）",
			before, added, len(pool.Subjects()))
	})
	return pool
}

// appendDir 把一个目录下的所有证书文件并入池；返回处理的文件数。
// Android 的 cacerts 目录里既有 PEM 也有 DER（老版本是 DER），两种都试。
func appendDir(pool *x509.CertPool, dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if pool.AppendCertsFromPEM(raw) {
			n++
			continue
		}
		// 不是 PEM：按 DER 解析（一条单证书）。
		if cert, err := x509.ParseCertificate(raw); err == nil {
			pool.AddCert(cert)
			n++
		}
	}
	return n, nil
}
