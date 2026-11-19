package com.wb2api.panel;

import android.app.Activity;
import android.content.Intent;
import android.graphics.Color;
import android.graphics.Typeface;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.text.TextUtils;
import android.util.TypedValue;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.webkit.CookieManager;
import android.webkit.JsResult;
import android.webkit.WebChromeClient;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.FrameLayout;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONTokener;

import java.util.LinkedHashMap;
import java.util.Map;

/**
 * 「网页登录取 Cookie」页：给签到中心填 Cookie 用。
 *
 * <p>手机上从浏览器导出 Cookie 基本不可行（Chrome 没有导出入口，第三方工具又要 root），
 * 所以把登录这一步搬进 App：这里的 WebView 正常登录站点，点「完成」时用
 * {@link CookieManager#getCookie(String)} 取该域下的 Cookie（含 HttpOnly，页面脚本拿不到的那种），
 * 连同 localStorage 快照一起回传给面板。Cookie 只在内存里交接，不落盘、不打日志。
 *
 * <p>为什么不复用面板那个 WebView：那是只允许 127.0.0.1 的受限实例，让它去加载任意外部站点
 * 会把「面板」和「不可信网页」混在同一个进程里；独立 Activity 的 WebView 生命周期更短，
 * 退出即销毁，风险面更小。
 */
public class LoginWebActivity extends Activity {

    static final String EXTRA_URL = "url";
    static final String EXTRA_TITLE = "title";
    static final String EXTRA_COOKIE = "cookie";
    static final String EXTRA_STORAGE = "storage";

    private static final int BG = 0xFF0C0E14;
    private static final int BAR = 0xFF10131A;
    private static final int LINE = 0xFF262C3A;
    private static final int INK = 0xFFE8EBF2;
    private static final int INK2 = 0xFFA8B0C2;
    private static final int ACCENT = 0xFF5B7CFA;

    /** 目标站点根地址（用户填的那个），Cookie 按它取；登录跳转后的地址只做兜底。 */
    private String boundUrl = "";
    private WebView web;
    private ProgressBar bar;

    private static final String LOCAL_STORAGE_JS =
            "(function(){try{var o={};for(var i=0;i<localStorage.length;i++){" +
            "var k=localStorage.key(i);o[k]=localStorage.getItem(k);}" +
            "return JSON.stringify(o);}catch(e){return \"{}\";}})()";

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        boundUrl = getIntent().getStringExtra(EXTRA_URL);
        String title = getIntent().getStringExtra(EXTRA_TITLE);
        if (boundUrl == null) boundUrl = "";
        if (!boundUrl.startsWith("http://") && !boundUrl.startsWith("https://")) {
            Toast.makeText(this, "网址格式不对，先填 https:// 开头的地址", Toast.LENGTH_SHORT).show();
            finish();
            return;
        }
        buildUi(TextUtils.isEmpty(title) ? "登录并获取 Cookie" : title);
        web.loadUrl(boundUrl);
    }

    /* ── 界面：顶栏（取消 / 标题 / 完成）+ 进度条 + WebView ────────────── */
    private void buildUi(String title) {
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setBackgroundColor(BG);

        LinearLayout nav = new LinearLayout(this);
        nav.setGravity(Gravity.CENTER_VERTICAL);
        nav.setBackgroundColor(BAR);
        nav.setPadding(dp(6), dp(4), dp(6), dp(4));

        Button cancel = new Button(this);
        cancel.setText("取消");
        cancel.setAllCaps(false);
        cancel.setTextColor(INK2);
        cancel.setBackgroundColor(Color.TRANSPARENT);
        cancel.setOnClickListener(v -> finish());
        nav.addView(cancel, new LinearLayout.LayoutParams(dp(64), dp(44)));

        TextView t = new TextView(this);
        t.setText(title);
        t.setTextColor(INK);
        t.setTextSize(TypedValue.COMPLEX_UNIT_SP, 14);
        t.setTypeface(Typeface.DEFAULT_BOLD);
        t.setSingleLine(true);
        t.setEllipsize(TextUtils.TruncateAt.END);
        t.setGravity(Gravity.CENTER);
        LinearLayout.LayoutParams tlp = new LinearLayout.LayoutParams(0, dp(44), 1f);
        nav.addView(t, tlp);

        Button done = new Button(this);
        done.setText("完成");
        done.setAllCaps(false);
        done.setTextColor(Color.WHITE);
        done.setBackgroundColor(ACCENT);
        done.setOnClickListener(v -> finishWithCredentials());
        LinearLayout.LayoutParams dlp = new LinearLayout.LayoutParams(dp(64), dp(38));
        dlp.rightMargin = dp(2);
        nav.addView(done, dlp);
        root.addView(nav, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        bar = new ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal);
        bar.setMax(100);
        bar.setProgress(0);
        bar.setVisibility(View.GONE);
        root.addView(bar, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, dp(2)));

        TextView tip = new TextView(this);
        tip.setText("登录成功后点右上角「完成」，App 会自动读取该站点的 Cookie");
        tip.setTextColor(INK2);
        tip.setTextSize(TypedValue.COMPLEX_UNIT_SP, 11);
        tip.setPadding(dp(12), dp(8), dp(12), dp(8));
        tip.setBackgroundColor(BG);
        root.addView(tip, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        FrameLayout wrap = new FrameLayout(this);
        web = new WebView(this);
        web.setBackgroundColor(BG);
        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setDatabaseEnabled(true);
        s.setSupportZoom(true);
        s.setBuiltInZoomControls(true);
        s.setDisplayZoomControls(false);
        s.setLoadWithOverviewMode(true);
        s.setUseWideViewPort(true);
        s.setTextZoom(100);
        s.setAllowFileAccess(false);
        s.setAllowContentAccess(false);
        // 不追加 App 的 UA 后缀：有些站点会按 UA 走不同的登录流程（甚至直接拒绝）。
        if (Build.VERSION.SDK_INT >= 29) s.setForceDark(WebSettings.FORCE_DARK_OFF);

        CookieManager cm = CookieManager.getInstance();
        cm.setAcceptCookie(true);
        if (Build.VERSION.SDK_INT >= 21) cm.setAcceptThirdPartyCookies(web, true);

        web.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView view, String url) {
                return openNonWeb(Uri.parse(url));
            }

            @Override
            public boolean shouldOverrideUrlLoading(WebView view, android.webkit.WebResourceRequest req) {
                return openNonWeb(req.getUrl());
            }
        });
        web.setWebChromeClient(new WebChromeClient() {
            @Override
            public void onProgressChanged(WebView view, int p) {
                bar.setVisibility(p >= 100 ? View.GONE : View.VISIBLE);
                bar.setProgress(p);
            }

            @Override
            public boolean onJsAlert(WebView view, String url, String message, JsResult result) {
                result.confirm();
                Toast.makeText(LoginWebActivity.this, message, Toast.LENGTH_SHORT).show();
                return true;
            }
        });
        wrap.addView(web, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        root.addView(wrap, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f));

        View divider = new View(this);
        divider.setBackgroundColor(LINE);
        root.addView(divider, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, dp(1)));

        setContentView(root);
        applyBars();
    }

    /** 自定义 scheme（微信/支付宝/APP 唤起）交给系统，别把登录页卡在错误页。 */
    private boolean openNonWeb(Uri uri) {
        if (uri == null) return false;
        String scheme = uri.getScheme();
        if ("http".equals(scheme) || "https".equals(scheme)) return false;
        try {
            startActivity(new Intent(Intent.ACTION_VIEW, uri));
        } catch (Exception ignored) {
        }
        return true;
    }

    private void applyBars() {
        getWindow().setStatusBarColor(BAR);
        getWindow().setNavigationBarColor(BAR);
        if (Build.VERSION.SDK_INT >= 30) {
            android.view.WindowInsetsController c = getWindow().getInsetsController();
            if (c != null) {
                // 深色底 → 浅色图标（清掉 LIGHT 位）
                c.setSystemBarsAppearance(0,
                        android.view.WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS
                                | android.view.WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS);
            }
        }
    }

    /**
     * 抓取凭证并回传：
     * <ul>
     *   <li>Cookie：按用户填的根地址取；若为空（登录后 cookie 落在跳转后的域名），再按当前地址取。</li>
     *   <li>localStorage：不少中转站把 access_token 放在 localStorage 里，Cookie 为空时靠它兜底。</li>
     * </ul>
     */
    private void finishWithCredentials() {
        String current = web.getUrl();
        CookieManager cm = CookieManager.getInstance();
        cm.flush();

        String cookie = cm.getCookie(boundUrl);
        if (TextUtils.isEmpty(cookie) && current != null) cookie = cm.getCookie(current);
        if (TextUtils.isEmpty(cookie) && current != null) {
            // 域名根（https://a.b.com/x → https://a.b.com/）再试一次：登录常跳子路径
            try {
                Uri u = Uri.parse(current);
                String root = u.getScheme() + "://" + u.getHost() + "/";
                cookie = cm.getCookie(root);
            } catch (Exception ignored) {
            }
        }
        if (TextUtils.isEmpty(cookie)) {
            Toast.makeText(this, "没取到 Cookie：确认已登录成功后再点完成", Toast.LENGTH_LONG).show();
            return;
        }

        final String cookieFinal = mergeCookies(cookie, current == null ? "" : cm.getCookie(current));
        web.evaluateJavascript(LOCAL_STORAGE_JS, value -> {
            String storage = decodeJsString(value);
            Intent data = new Intent();
            data.putExtra(EXTRA_COOKIE, cookieFinal);
            data.putExtra(EXTRA_STORAGE, storage);
            setResult(RESULT_OK, data);
            finish();
        });
    }

    /** 两个来源的同名 Cookie 只保留一个（按 name= 去重），避免后端收到重复键。 */
    private String mergeCookies(String a, String b) {
        Map<String, String> seen = new LinkedHashMap<>();
        for (String src : new String[]{a, b}) {
            if (TextUtils.isEmpty(src)) continue;
            for (String part : src.split(";")) {
                String p = part.trim();
                if (p.isEmpty()) continue;
                int eq = p.indexOf('=');
                String name = eq > 0 ? p.substring(0, eq) : p;
                if (!seen.containsKey(name)) seen.put(name, p);
            }
        }
        StringBuilder sb = new StringBuilder();
        for (String v : seen.values()) {
            if (sb.length() > 0) sb.append("; ");
            sb.append(v);
        }
        return sb.toString();
    }

    /** evaluateJavascript 回的是 JSON 字符串字面量（带引号与转义）；解一层拿到原文。 */
    private String decodeJsString(String value) {
        if (value == null) return "{}";
        try {
            Object o = new JSONTokener(value).nextValue();
            if (o instanceof String) return (String) o;
        } catch (Exception ignored) {
        }
        return "{}";
    }

    private int dp(float v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }

    @Override
    public void onBackPressed() {
        if (web != null && web.canGoBack()) {
            web.goBack();
            return;
        }
        super.onBackPressed();
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