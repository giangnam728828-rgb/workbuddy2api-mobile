package com.wb2api.panel;

import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.graphics.Typeface;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowInsetsController;
import android.webkit.JavascriptInterface;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.FrameLayout;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import java.net.HttpURLConnection;
import java.net.URL;

/**
 * 单 Activity 的壳：启动内置 Go 网关 → 等它监听 127.0.0.1 → 用 WebView 打开面板。
 *
 * <p>设计要点：
 * <ul>
 *   <li>不使用 AndroidX，纯 framework，APK 小、启动快、依赖少；</li>
 *   <li>WebView 只允许加载本机回环地址，其余链接（OAuth 授权页等）交给系统浏览器；</li>
 *   <li>返回键顺序：关弹层 → 关抽屉 → 回首页 → 退到后台（不杀服务）；</li>
 *   <li>面板明暗主题通过 JS 桥回传，状态栏/导航栏跟着变，避免割裂感。</li>
 * </ul>
 */
public class MainActivity extends Activity {

    private static final int REQ_NOTIF = 1001;
    private static final int DARK_BG = 0xFF0C0E14;
    private static final int DARK_BAR = 0xFF10131A;
    private static final int LIGHT_BG = 0xFFF4F5F8;
    private static final int LIGHT_BAR = 0xFFFFFFFF;

    private FrameLayout root;
    private LinearLayout splash;
    private ProgressBar spinner;
    private TextView statusView;
    private WebView web;
    private int port = 7863;
    private String baseUrl = "http://127.0.0.1:7863";
    private boolean keyChecked = false;
    private boolean panelShown = false;
    private boolean darkTheme = true;
    private final Handler ui = new Handler(Looper.getMainLooper());

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        root = new FrameLayout(this);
        root.setBackgroundColor(DARK_BG);
        buildSplash();
        setContentView(root);

        applyTheme(true);

        if (Build.VERSION.SDK_INT >= 33
                && checkSelfPermission("android.permission.POST_NOTIFICATIONS") != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{"android.permission.POST_NOTIFICATIONS"}, REQ_NOTIF);
        }

        Intent svc = new Intent(this, ServerService.class);
        if (Build.VERSION.SDK_INT >= 26) startForegroundService(svc);
        else startService(svc);

        boot();
    }

    /* ── 启动画面 ────────────────────────────────────────────────────── */
    private void buildSplash() {
        splash = new LinearLayout(this);
        splash.setOrientation(LinearLayout.VERTICAL);
        splash.setGravity(Gravity.CENTER);
        splash.setBackgroundColor(DARK_BG);
        int pad = dp(28);
        splash.setPadding(pad, pad, pad, pad);

        ImageView icon = new ImageView(this);
        icon.setImageResource(R.mipmap.ic_launcher);
        LinearLayout.LayoutParams ilp = new LinearLayout.LayoutParams(dp(84), dp(84));
        ilp.gravity = Gravity.CENTER_HORIZONTAL;
        splash.addView(icon, ilp);

        TextView title = new TextView(this);
        title.setText("WorkBuddy2API");
        title.setTextColor(0xFFE8EBF2);
        title.setTextSize(TypedValue.COMPLEX_UNIT_SP, 20);
        title.setTypeface(Typeface.DEFAULT_BOLD);
        title.setGravity(Gravity.CENTER);
        LinearLayout.LayoutParams tlp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        tlp.topMargin = dp(16);
        splash.addView(title, tlp);

        TextView sub = new TextView(this);
        sub.setText("本机网关 · 手机版");
        sub.setTextColor(0xFF6B7488);
        sub.setTextSize(TypedValue.COMPLEX_UNIT_SP, 12.5f);
        sub.setGravity(Gravity.CENTER);
        LinearLayout.LayoutParams slp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        slp.topMargin = dp(4);
        splash.addView(sub, slp);

        spinner = new ProgressBar(this);
        LinearLayout.LayoutParams plp = new LinearLayout.LayoutParams(dp(26), dp(26));
        plp.topMargin = dp(26);
        plp.gravity = Gravity.CENTER_HORIZONTAL;
        splash.addView(spinner, plp);

        statusView = new TextView(this);
        statusView.setText("正在启动内置网关…");
        statusView.setTextColor(0xFFA8B0C2);
        statusView.setTextSize(TypedValue.COMPLEX_UNIT_SP, 12);
        statusView.setGravity(Gravity.CENTER);
        LinearLayout.LayoutParams stlp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        stlp.topMargin = dp(12);
        splash.addView(statusView, stlp);

        root.addView(splash, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
    }

    private void setStatus(String s) {
        ui.post(() -> {
            if (statusView != null) statusView.setText(s);
        });
    }

    /* ── 启动流程：解析配置 → 等端口 → 建 WebView ─────────────────────── */
    private void boot() {
        new Thread(() -> {
            try {
                String[] cfg = ServerRunner.prepare(this);
                port = Integer.parseInt(cfg[1]);
                baseUrl = "http://127.0.0.1:" + port;
                setStatus("正在启动内置网关…");
                ServerRunner.ensureStarted(this);

                boolean ok = false;
                for (int i = 0; i < 160; i++) {           // 最多等 40s（首次启动要建索引/读 auths）
                    if (ServerRunner.isDead()) break;
                    if (healthy()) {
                        ok = true;
                        break;
                    }
                    if (i == 20) setStatus("正在连接上游账号池…");
                    if (i == 60) setStatus("首次启动较慢，请稍候…");
                    Thread.sleep(250);
                }
                // 兜底放行：只要端口已经在监听，就先进面板。
                // 面板自己会把「没有账号」「密钥不对」显示清楚，比把用户锁在启动页强；
                // 之前正是缺这一层，才会出现「日志已 listening、App 却报超时」。
                if (!ok && portOpen()) ok = true;

                if (ok) {
                    ui.post(this::createWebView);
                } else {
                    showError(ServerRunner.isDead()
                            ? "内置网关已退出。\n\n" + ServerRunner.tail(1200)
                            : "内置网关启动超时。\n\n" + ServerRunner.tail(1200));
                }
            } catch (Throwable t) {
                showError("启动失败：" + t + "\n\n" + ServerRunner.tail(800));
            }
        }, "wb2api-boot").start();
    }

    /**
     * 判断「内置网关是否已经可以打开面板」。
     *
     * 注意这里刻意不要求账号池可用：网关在没有任何账号时（全新安装、账号被删光）
     * /healthz 返回 503 —— 那是「当前没有可服务账号」的业务语义，不是「服务没起来」。
     * 早期版本只认 200，于是空账号池的首次启动一定卡满 40 秒再报「启动超时」，
     * 而日志里明明已经 listening。200 与 503 都视为就绪。
     */
    private boolean healthy() {
        HttpURLConnection c = null;
        try {
            c = (HttpURLConnection) new URL(baseUrl + "/healthz").openConnection();
            c.setConnectTimeout(1000);
            c.setReadTimeout(1000);
            c.setUseCaches(false);
            int code = c.getResponseCode();
            return code == 200 || code == 503;
        } catch (Exception e) {
            return false;
        } finally {
            if (c != null) c.disconnect();
        }
    }

    /** 端口是否已被监听：探活口径以后若再变，也不至于把可用的服务判死。 */
    private boolean portOpen() {
        try (java.net.Socket s = new java.net.Socket()) {
            s.connect(new java.net.InetSocketAddress("127.0.0.1", port), 500);
            return true;
        } catch (Exception e) {
            return false;
        }
    }

    private void showError(String msg) {
        ui.post(() -> {
            // 面板已挂到 root 上时会盖住 splash：先摘掉，错误页才看得见；
            // 重试走 recreate()，会重新建 WebView。
            if (web != null) {
                root.removeView(web);
                web.destroy();
                web = null;
                keyChecked = false;
                panelShown = false;
            }
            splash.setVisibility(View.VISIBLE);
            splash.removeAllViews();
            splash.setPadding(dp(24), dp(24), dp(24), dp(24));

            TextView t = new TextView(this);
            t.setText("无法启动网关");
            t.setTextColor(0xFFF0655F);
            t.setTextSize(TypedValue.COMPLEX_UNIT_SP, 17);
            t.setTypeface(Typeface.DEFAULT_BOLD);
            t.setGravity(Gravity.CENTER);
            splash.addView(t);

            TextView d = new TextView(this);
            d.setText(msg);
            d.setTextColor(0xFFA8B0C2);
            d.setTextSize(TypedValue.COMPLEX_UNIT_SP, 11.5f);
            d.setGravity(Gravity.START);
            d.setTextIsSelectable(true);
            ScrollView sc = new ScrollView(this);
            sc.addView(d);
            LinearLayout.LayoutParams dlp = new LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f);
            dlp.topMargin = dp(14);
            splash.addView(sc, dlp);

            Button retry = new Button(this);
            retry.setText("重试");
            retry.setOnClickListener(v -> recreate());
            LinearLayout.LayoutParams rlp = new LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.MATCH_PARENT, dp(44));
            rlp.topMargin = dp(12);
            splash.addView(retry, rlp);
        });
    }

    private void createWebView() {
        if (web != null) return;
        web = new WebView(this);
        web.setBackgroundColor(DARK_BG);
        web.setAlpha(0f);

        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setDatabaseEnabled(true);
        s.setSupportZoom(false);
        s.setBuiltInZoomControls(false);
        s.setDisplayZoomControls(false);
        s.setTextZoom(100);                    // 忽略系统字体缩放，保持面板栅格不被撑破
        s.setLoadWithOverviewMode(false);
        s.setUseWideViewPort(true);            // 让页面按 device-width 排版（不是 980px 视口）
        s.setSupportMultipleWindows(false);    // window.open 走同页导航 → 被 shouldOverrideUrlLoading 接管
        s.setMediaPlaybackRequiresUserGesture(true);
        s.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        s.setAllowFileAccess(false);
        s.setAllowContentAccess(false);
        s.setGeolocationEnabled(false);
        s.setCacheMode(WebSettings.LOAD_DEFAULT);
        s.setUserAgentString(s.getUserAgentString() + " WorkBuddy2APIApp/1.0");
        if (Build.VERSION.SDK_INT >= 29) {
            s.setForceDark(WebSettings.FORCE_DARK_OFF);   // 面板自己管明暗，禁止系统二次压暗
        }

        web.addJavascriptInterface(new Bridge(), "AndroidBridge");

        web.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest req) {
                return handleUrl(req.getUrl());
            }

            @Override
            @SuppressWarnings("deprecation")
            public boolean shouldOverrideUrlLoading(WebView view, String url) {
                return handleUrl(Uri.parse(url));
            }

            @Override
            public void onReceivedError(WebView view, WebResourceRequest req, WebResourceError err) {
                // 只处理主文档失败；图片/字体等子资源失败不影响面板使用。
                // 走 showError 而不是让 WebView 显示自带错误页：中文诊断 + 内核日志更可查。
                if (req != null && req.isForMainFrame()) {
                    showError("面板加载失败（" + err.getErrorCode() + "）：" + err.getDescription()
                            + "\n\n" + ServerRunner.tail(800));
                }
            }

            @Override
            public void onPageFinished(WebView view, String url) {
                if (!url.startsWith(baseUrl)) return;
                if (!keyChecked) {
                    keyChecked = true;
                    // 把配置里的 api_key 直接写进 localStorage：用户打开 App 就是登录态，
                    // 不会先被「输入 API 密钥」挡一道。密钥变了就重写并重载一次。
                    String key = ServerRunner.apiKey();
                    view.evaluateJavascript("localStorage.getItem('wb2api.key')", cur -> {
                        String want = "\"" + key + "\"";
                        if (key.isEmpty() || want.equals(cur)) {
                            revealPanel();
                        } else {
                            view.evaluateJavascript(
                                    "localStorage.setItem('wb2api.key','" + key.replace("'", "") + "');location.reload();", null);
                        }
                    });
                } else {
                    revealPanel();
                }
            }
        });

        web.setWebChromeClient(new WebChromeClient() {
            @Override
            public boolean onCreateWindow(WebView view, boolean isDialog, boolean isUserGesture, android.os.Message resultMsg) {
                // 面板里的 window.open（授权页等）一律交给系统浏览器，不在 WebView 里开新窗口
                WebView.HitTestResult r = view.getHitTestResult();
                if (r != null && r.getExtra() != null) openExternal(r.getExtra());
                return false;
            }
        });

        web.setDownloadListener((url, ua, disposition, mime, len) -> openExternal(url));

        root.addView(web, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        web.loadUrl(baseUrl + "/panel/");
    }

    /** 只在本机回环内导航；外部链接（OAuth 授权、官方文档）交给系统浏览器。 */
    private boolean handleUrl(Uri uri) {
        if (uri == null) return false;
        String host = uri.getHost();
        String scheme = uri.getScheme();
        if ("http".equals(scheme) || "https".equals(scheme)) {
            if ("127.0.0.1".equals(host) || "localhost".equals(host)) return false;
            // OAuth 授权页、官方文档等：交给系统浏览器，并提示用户回来
            Toast.makeText(this, "已用浏览器打开，完成后返回本应用", Toast.LENGTH_SHORT).show();
            openExternal(uri.toString());
            return true;
        }
        try {
            startActivity(new Intent(Intent.ACTION_VIEW, uri));
        } catch (Exception ignored) {
        }
        return true;
    }

    private void openExternal(String url) {
        try {
            Intent i = new Intent(Intent.ACTION_VIEW, Uri.parse(url));
            i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
            startActivity(i);
        } catch (Exception e) {
            Toast.makeText(this, "没有可打开该链接的应用", Toast.LENGTH_SHORT).show();
        }
    }

    private void revealPanel() {
        if (panelShown) return;
        panelShown = true;
        splash.setVisibility(View.GONE);
        web.animate().alpha(1f).setDuration(180).start();
    }

    /* ── 返回键：逐层后退，退到底就退到后台（服务继续跑） ──────────────── */
    @Override
    public void onBackPressed() {
        if (web == null || !panelShown) {
            super.onBackPressed();
            return;
        }
        web.evaluateJavascript("(window.__wbBack?__wbBack():false)", value -> {
            if (!"true".equals(value)) {
                // 不 finish()：退到后台，网关继续服务（通知栏可一键停止）
                moveTaskToBack(true);
            }
        });
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (web != null && panelShown) {
            web.evaluateJavascript("window.__wbOnResume&&window.__wbOnResume()", null);
        }
    }

    /* ── 主题：面板改明暗 → 原生状态栏/导航栏同步 ─────────────────────── */
    private void applyTheme(boolean dark) {
        darkTheme = dark;
        int bg = dark ? DARK_BG : LIGHT_BG;
        int bar = dark ? DARK_BAR : LIGHT_BAR;
        getWindow().setStatusBarColor(bar);
        getWindow().setNavigationBarColor(bar);
        root.setBackgroundColor(bg);
        if (splash != null) splash.setBackgroundColor(bg);
        if (web != null) web.setBackgroundColor(bg);

        boolean lightIcons = dark;             // 深色底要浅色图标
        if (Build.VERSION.SDK_INT >= 30) {
            WindowInsetsController c = getWindow().getInsetsController();
            if (c != null) {
                int mask = WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS
                        | WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS;
                c.setSystemBarsAppearance(lightIcons ? 0 : mask, mask);
            }
        } else {
            int flags = getWindow().getDecorView().getSystemUiVisibility();
            if (!lightIcons) {
                flags |= View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
                if (Build.VERSION.SDK_INT >= 26) flags |= View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
            } else {
                flags &= ~View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
                if (Build.VERSION.SDK_INT >= 26) flags &= ~View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
            }
            getWindow().getDecorView().setSystemUiVisibility(flags);
        }
    }

    /** JS → 原生桥（只暴露给本机面板页面）。 */
    private class Bridge {
        @JavascriptInterface
        public void setTheme(String theme) {
            boolean dark = !"light".equals(theme);
            if (dark != darkTheme) ui.post(() -> applyTheme(dark));
        }

        @JavascriptInterface
        public String platform() {
            return "android";
        }

        @JavascriptInterface
        public String version() {
            return BuildConfig.VERSION_NAME;
        }

        @JavascriptInterface
        public void openExternal(String url) {
            if (url != null) ui.post(() -> MainActivity.this.openExternal(url));
        }

        @JavascriptInterface
        public void restartServer() {
            ui.post(() -> {
                ServerRunner.stop();
                recreate();
            });
        }
    }

    private int dp(float v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }

    @Override
    protected void onDestroy() {
        if (web != null) {
            web.destroy();
            web = null;
        }
        super.onDestroy();
    }
}
