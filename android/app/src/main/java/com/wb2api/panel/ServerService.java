package com.wb2api.panel;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.os.Build;
import android.os.IBinder;

/**
 * 前台服务：让内置网关在 App 退到后台/锁屏后继续跑。
 *
 * <p>Android 8+ 后台进程会被限流甚至冻结，没有前台服务的话，手机一锁屏网关就停了，
 * 其它 App 请求 127.0.0.1 会全部超时。常驻通知就是这个服务的"价格"，也是用户
 * 明确知道自己开着网关的唯一入口（点通知回面板，通知里可直接停止）。
 */
public class ServerService extends Service {

    public static final String ACTION_STOP = "com.wb2api.panel.STOP";
    private static final String CHANNEL_ID = "gateway";
    private static final int NOTIF_ID = 7863;

    @Override
    public void onCreate() {
        super.onCreate();
        createChannel();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        String action = intent == null ? null : intent.getAction();
        if (ACTION_STOP.equals(action)) {
            ServerRunner.stop();
            stopForeground(true);
            stopSelf();
            return START_NOT_STICKY;
        }

        startForeground(NOTIF_ID, buildNotification());
        try {
            String[] cfg = ServerRunner.prepare(this);
            ServerRunner.ensureStarted(this);
            updateNotification(Integer.parseInt(cfg[1]));
        } catch (Throwable t) {
            // 启动失败不在这里弹 UI：界面侧会展示错误详情，服务保持前台让用户能点通知回到 App
            updateNotification(7863);
        }
        // START_STICKY：系统回收后自动拉起（配置与账号都在磁盘上，重启无副作用）
        return START_STICKY;
    }

    private void createChannel() {
        if (Build.VERSION.SDK_INT < 26) return;
        NotificationManager nm = getSystemService(NotificationManager.class);
        if (nm == null) return;
        NotificationChannel ch = new NotificationChannel(
                CHANNEL_ID, getString(R.string.notif_channel), NotificationManager.IMPORTANCE_MIN);
        ch.setDescription(getString(R.string.notif_channel_desc));
        ch.setShowBadge(false);
        ch.setSound(null, null);
        ch.enableVibration(false);
        nm.createNotificationChannel(ch);
    }

    private Notification buildNotification() {
        return buildNotification(7863);
    }

    private void updateNotification(int port) {
        NotificationManager nm = (NotificationManager) getSystemService(NOTIFICATION_SERVICE);
        if (nm != null) nm.notify(NOTIF_ID, buildNotification(port));
    }

    private Notification buildNotification(int port) {
        Intent open = new Intent(this, MainActivity.class);
        open.setFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP | Intent.FLAG_ACTIVITY_NEW_TASK);
        PendingIntent pi = PendingIntent.getActivity(this, 0, open, pendingFlags());

        Intent stop = new Intent(this, ServerService.class).setAction(ACTION_STOP);
        PendingIntent stopPi = PendingIntent.getService(this, 1, stop, pendingFlags());

        Notification.Builder b = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(this, CHANNEL_ID)
                : new Notification.Builder(this);
        b.setContentTitle(getString(R.string.notif_title))
                .setContentText(getString(R.string.notif_text, port))
                .setSmallIcon(R.drawable.ic_stat)
                .setContentIntent(pi)
                .setOngoing(true)
                .setShowWhen(false)
                .addAction(new Notification.Action.Builder(null, "停止", stopPi).build());
        if (Build.VERSION.SDK_INT >= 21) b.setColor(0xFF5B7CFA);
        return b.build();
    }

    private int pendingFlags() {
        int f = PendingIntent.FLAG_UPDATE_CURRENT;
        if (Build.VERSION.SDK_INT >= 23) f |= PendingIntent.FLAG_IMMUTABLE;
        return f;
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    @Override
    public void onTaskRemoved(Intent rootIntent) {
        // 用户从最近任务里划掉 App：保持网关常驻（通知栏还在，可手动停）
        super.onTaskRemoved(rootIntent);
    }
}
