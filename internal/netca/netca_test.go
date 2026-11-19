package netca

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// Android 上 Go 的 SystemCertPool 是空池，全靠内嵌基线兜底。
// 这条测试钉住「基线真的在、而且够用」：一旦有人删了 certs.pem 或改坏嵌入，
// 手机上所有 HTTPS 会立刻死在 unknown authority，且只在真机才暴露。
func TestPoolHasEmbeddedRoots(t *testing.T) {
	pool := Pool()
	if pool == nil {
		t.Fatal("Pool() 返回 nil")
	}
	subjects := pool.Subjects()
	// Mozilla 基线有 100+ 条；门槛放在 50 足够识别「文件丢了/嵌入失败」。
	if len(subjects) < 50 {
		t.Fatalf("根证书数量异常偏少（%d 条）：内嵌基线可能没生效", len(subjects))
	}
}

// 内嵌文件必须是可解析的 PEM 证书集合。
func TestEmbeddedBundleParses(t *testing.T) {
	rest := embeddedPEM
	count := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			t.Fatalf("内嵌文件里出现非证书块：%s", block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			t.Fatalf("第 %d 张证书解析失败：%v", count+1, err)
		}
		count++
	}
	if count < 50 {
		t.Fatalf("内嵌证书只有 %d 张，疑似文件被截断", count)
	}
}

// Android 的 cacerts 目录里 PEM 与 DER 混放；两种都要能读进来。
func TestAppendDirHandlesPEMAndDER(t *testing.T) {
	dir := t.TempDir()

	// 取内嵌基线的第一张证书，分别以 PEM / DER 落盘。
	block, _ := pem.Decode(embeddedPEM)
	if block == nil {
		t.Fatal("内嵌基线里没有证书")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("解析首张证书失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pem-one"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "der-one"), cert.Raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// 一个不是证书的文件：不能让它中断扫描。
	if err := os.WriteFile(filepath.Join(dir, "garbage"), []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	n, err := appendDir(pool, dir)
	if err != nil {
		t.Fatalf("appendDir: %v", err)
	}
	if n != 2 {
		t.Fatalf("应读入 2 个证书文件（PEM + DER），实际 %d", n)
	}
}

// 目录不存在时不能报错打断（桌面上没有 Android 目录）。
func TestAppendDirMissingIsSilent(t *testing.T) {
	pool := x509.NewCertPool()
	n, err := appendDir(pool, "/nonexistent/cacerts")
	if err == nil {
		t.Fatal("目录不存在应返回 error 供调用方忽略")
	}
	if n != 0 {
		t.Fatalf("目录不存在时不应读入任何证书，实际 %d", n)
	}
}
