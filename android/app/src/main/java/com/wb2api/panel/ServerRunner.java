package com.wb2api.panel;

import android.content.Context;
import android.os.Build;
import android.util.Log;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * 内置网关进程的生命周期与配置文件准备。
 *
 * <p>Go 二进制以 {@code libwb2api.so} 的名字放在 jniLibs 里，安装时被解压到
 * {@code nativeLibraryDir}。这是 Android 10+ 唯一允许直接 exec 的位置（应用数据目录
 * 被 W^X 策略禁止执行），因此不能把二进制塞进 assets 再复制出来跑。
 */
public final class ServerRunner {

    private static final String TAG = "WB2API";
    private static final String BIN_NAME = "libwb2api.so";
    private static final int DEFAULT_PORT = 7863;
    private static final String DEFAULT_DNS = "[\"223.5.5.5\",\"119.29.29.29\"]";

    private static Process proc;
    private static volatile int exitCode = Integer.MIN_VALUE;
    private static final StringBuilder LOG = new StringBuilder();
    private static String apiKey = "";

    private ServerRunner() {
    }

    /* ── 配置 ────────────────────────────────────────────────────────── */

    /**
     * 确保 config.json / auths / data 就绪，返回 {apiKey, port}。
     * 缺配置就生成一份手机端推荐配置（含 DNS、随机 api_key）。
     */
    public static synchronized String[] prepare(Context ctx) throws IOException {
        File dir = ctx.getFilesDir();
        File auths = new File(dir, "auths");
        File data = new File(dir, "data");
        if (!auths.exists() && !auths.mkdirs()) throw new IOException("无法创建 auths 目录");
        if (!data.exists() && !data.mkdirs()) throw new IOException("无法创建 data 目录");

        File cfg = new File(dir, "config.json");
        if (!cfg.exists()) {
            writeDefault(cfg);
            log("已生成默认配置 " + cfg.getAbsolutePath());
        } else {
            patchDns(cfg);
        }

        String text = read(cfg);
        apiKey = firstGroup(text, "\"api_key\"\\s*:\\s*\"([^\"]*)\"");
        String listen = firstGroup(text, "\"listen\"\\s*:\\s*\"([^\"]*)\"");
        int port = portOf(listen);
        return new String[]{apiKey, String.valueOf(port)};
    }

    public static String apiKey() {
        return apiKey;
    }

    private static int portOf(String listen) {
        if (listen == null || listen.isEmpty()) return DEFAULT_PORT;
        int i = listen.lastIndexOf(':');
        try {
            String p = i >= 0 ? listen.substring(i + 1) : listen;
            int v = Integer.parseInt(p.trim());
            return (v > 0 && v < 65536) ? v : DEFAULT_PORT;
        } catch (Exception e) {
            return DEFAULT_PORT;
        }
    }

    private static void writeDefault(File cfg) throws IOException {
        String key = randomKey();
        String json = "{\n"
                + "  \"listen\": \"127.0.0.1:" + DEFAULT_PORT + "\",\n"
                + "  \"api_key\": \"" + key + "\",\n"
                + "  \"auth_dir\": \"./auths\",\n"
                + "  \"state_file\": \"./data/state.json\",\n"
                // Android 没有 /etc/resolv.conf，Go 会退回 127.0.0.1:53 导致全部域名解析失败，
                // 必须显式给 DNS。阿里 + 腾讯两家，任意一家可达即可。
                + "  \"dns_servers\": " + DEFAULT_DNS + ",\n"
                + "  \"server\": { \"read_timeout\": \"300s\" },\n"
                + "  \"schedule\": {\n"
                + "    \"checkin_enabled\": true, \"growth_enabled\": true, \"travel_enabled\": true,\n"
                + "    \"activity_enabled\": true, \"keepalive_enabled\": true, \"blackcat_enabled\": true\n"
                + "  }\n"
                + "}\n";
        write(cfg, json);
    }

    /** 面板「配置」页保存时可能把 dns_servers 写成空数组 —— 手机上那等于断网，这里补回默认值。 */
    private static void patchDns(File cfg) throws IOException {
        String text = read(cfg);
        String patched = text;
        if (Pattern.compile("\"dns_servers\"\\s*:\\s*\\[\\s*\\]").matcher(patched).find()) {
            patched = patched.replaceFirst("\"dns_servers\"\\s*:\\s*\\[\\s*\\]", "\"dns_servers\": " + DEFAULT_DNS);
        } else if (!patched.contains("\"dns_servers\"")) {
            int i = patched.indexOf('{');
            if (i >= 0) {
                patched = patched.substring(0, i + 1) + "\n  \"dns_servers\": " + DEFAULT_DNS + ","
                        + patched.substring(i + 1);
            }
        }
        if (!patched.equals(text)) {
            write(cfg, patched);
            log("已为 Android 环境补齐 dns_servers");
        }
    }

    /* ── 进程 ────────────────────────────────────────────────────────── */

    public static synchronized void ensureStarted(Context ctx) throws IOException {
        if (proc != null && proc.isAlive()) return;

        File bin = new File(ctx.getApplicationInfo().nativeLibraryDir, BIN_NAME);
        if (!bin.exists() || !bin.canExecute()) {
            throw new IOException("未找到当前设备可用的网关内核（" + Build.SUPPORTED_ABIS[0] + "）。\n"
                    + "请确认安装的是包含该 ABI 的 APK，或重新下载 arm64-v8a 版本。");
        }

        File cfg = new File(ctx.getFilesDir(), "config.json");
        ProcessBuilder pb = new ProcessBuilder(bin.getAbsolutePath(), "-config", cfg.getAbsolutePath());
        pb.directory(ctx.getFilesDir());
        pb.redirectErrorStream(true);
        Map<String, String> env = pb.environment();
        // Android 没有 /usr/share/zoneinfo：用 POSIX 时区串（UTC+8 无夏令时），
        // 否则定时任务会按 UTC 触发，签到/旅行时间全错。
        // TZ 用 IANA 名称，不用 POSIX 串：Go 不解析 "CST-8"，而 Android 没有
        // /usr/share/zoneinfo，两者叠加会让进程静默退回 UTC，所有定点任务错 8 小时。
        // 内核已内嵌 time/tzdata，所以这里只要能给出 IANA 名称就能解析成功。
        env.put("TZ", "Asia/Shanghai");
        env.put("HOME", ctx.getFilesDir().getAbsolutePath());
        env.put("TMPDIR", ctx.getCacheDir().getAbsolutePath());
        env.put("GODEBUG", "netdns=go");

        exitCode = Integer.MIN_VALUE;
        proc = pb.start();
        log("已启动内核（" + Build.SUPPORTED_ABIS[0] + "）");

        final Process p = proc;
        Thread t = new Thread(() -> {
            try (BufferedReader r = new BufferedReader(
                    new InputStreamReader(p.getInputStream(), StandardCharsets.UTF_8))) {
                String line;
                while ((line = r.readLine()) != null) {
                    log(line);
                    Log.i(TAG, line);
                }
            } catch (Exception e) {
                log("[读取日志失败] " + e);
            }
            try {
                exitCode = p.waitFor();
            } catch (InterruptedException ignored) {
            }
            if (exitCode != 0) log("内核已退出，退出码 " + exitCode);
        }, "wb2api-log");
        t.setDaemon(true);
        t.start();
    }

    public static synchronized boolean isDead() {
        return proc != null && !proc.isAlive();
    }

    public static synchronized void stop() {
        if (proc != null) {
            log("主动停止内核");
            proc.destroy();
            proc = null;
        }
    }

    public static synchronized String tail(int max) {
        String s = LOG.toString();
        return s.length() <= max ? s : s.substring(s.length() - max);
    }

    private static synchronized void log(String line) {
        LOG.append(line).append('\n');
        if (LOG.length() > 96 * 1024) LOG.delete(0, LOG.length() - 64 * 1024);   // 只留最近 64KB
    }

    /* ── 小工具 ──────────────────────────────────────────────────────── */

    private static String randomKey() {
        byte[] b = new byte[16];
        new SecureRandom().nextBytes(b);
        StringBuilder sb = new StringBuilder();
        for (byte x : b) sb.append(String.format("%02x", x));
        return sb.toString();
    }

    private static String read(File f) throws IOException {
        byte[] buf = new byte[(int) Math.max(1, f.length())];
        try (java.io.FileInputStream in = new java.io.FileInputStream(f)) {
            int off = 0, n;
            while (off < buf.length && (n = in.read(buf, off, buf.length - off)) > 0) off += n;
        }
        return new String(buf, StandardCharsets.UTF_8);
    }

    private static void write(File f, String s) throws IOException {
        try (FileOutputStream out = new FileOutputStream(f)) {
            out.write(s.getBytes(StandardCharsets.UTF_8));
        }
    }

    private static String firstGroup(String text, String re) {
        Matcher m = Pattern.compile(re).matcher(text);
        return m.find() ? m.group(1) : "";
    }
}
