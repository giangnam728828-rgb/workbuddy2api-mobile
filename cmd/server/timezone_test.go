package main

import (
	"os"
	"testing"
	"time"
)

// 时区解析必须能在「没有 /usr/share/zoneinfo」的环境里工作。
//
// 背景：Android 不带 zoneinfo 目录，Go 找不到时区数据时会**静默**退回 UTC，
// 进程照常跑、日志照常输出，只是所有定点任务（签到 / 旅行 / 保活 / 夜猫子 /
// 自动签到）整体错 8 小时。之前用的 TZ=CST-8 更是双重踩坑：Go 不解析 POSIX
// 时区串，所以哪怕有 zoneinfo 也解析不出来。
//
// 修法是 import _ "time/tzdata" 把 IANA 库内嵌进二进制。这条测试就钉住它：
// 把 ZONEINFO 指到不存在的路径、并逐个加载，确认仍能解析出 +08:00。
func TestEmbeddedTimeZoneAvailable(t *testing.T) {
	// ZONEINFO 优先级高于内嵌库；指向不存在的目录以确认我们不是靠系统文件。
	t.Setenv("ZONEINFO", "/nonexistent-zoneinfo-for-test")

	for _, name := range []string{"Asia/Shanghai", "Asia/Tokyo", "America/New_York"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("加载时区 %s 失败（time/tzdata 没内嵌？）：%v", name, err)
		}
		// 取一个具体时刻判断偏移：夏令时会让 1 月的 New_York 是 -5h。
		when := time.Date(2026, 1, 15, 12, 0, 0, 0, loc)
		_, off := when.Zone()
		switch name {
		case "Asia/Shanghai":
			// 中国不实行夏令时，全年 +8h。
			if off != 8*3600 {
				t.Fatalf("Asia/Shanghai 偏移应为 +08:00，实际 %d 秒", off)
			}
		case "Asia/Tokyo":
			if off != 9*3600 {
				t.Fatalf("Asia/Tokyo 偏移应为 +09:00，实际 %d 秒", off)
			}
		case "America/New_York":
			if off != -5*3600 {
				t.Fatalf("America/New_York 1 月偏移应为 -05:00，实际 %d 秒", off)
			}
		}
	}
}

// 手机端设的 TZ 必须是能被解析的 IANA 名称。
// 这条防的是「有人把 ServerRunner 改回 TZ=CST-8」——那种写法在 App 里是静默失效。
func TestAndroidTZIsIANAName(t *testing.T) {
	raw, err := os.ReadFile("../../android/app/src/main/java/com/wb2api/panel/ServerRunner.java")
	if err != nil {
		t.Skipf("读不到 ServerRunner.java（不在仓库根运行？）：%v", err)
	}
	src := string(raw)
	if containsAny(src, `env.put("TZ", "CST-8")`) {
		t.Fatal(`ServerRunner 用了 POSIX 时区串 "CST-8"：Go 不解析它，App 内会静默退回 UTC`)
	}
	if !containsAny(src, `env.put("TZ", "Asia/Shanghai")`) {
		t.Fatal(`ServerRunner 没把 TZ 设成 Asia/Shanghai`)
	}
	// 同一个名字必须真的能解析（与上面那条测试互补：那条测库，这条测配置）。
	if _, err := time.LoadLocation("Asia/Shanghai"); err != nil {
		t.Fatalf("配置里用的时区名解析不了：%v", err)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
