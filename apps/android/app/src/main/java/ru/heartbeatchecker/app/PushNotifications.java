package ru.heartbeatchecker.app;

import android.Manifest;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;

import org.json.JSONException;
import org.json.JSONObject;

final class PushNotifications {
    static final String CHANNEL = "incidents";
    private PushNotifications() {}

    static void channel(Context context) {
        NotificationChannel channel = new NotificationChannel(CHANNEL, "Инциденты", NotificationManager.IMPORTANCE_DEFAULT);
        channel.setDescription("Сбои и восстановление ваших мониторов");
        channel.setLockscreenVisibility(Notification.VISIBILITY_PRIVATE);
        context.getSystemService(NotificationManager.class).createNotificationChannel(channel);
    }

    static boolean permitted(Context context) {
        channel(context);
        NotificationManager manager = context.getSystemService(NotificationManager.class);
        NotificationChannel channel = manager.getNotificationChannel(CHANNEL);
        return (Build.VERSION.SDK_INT < 33 || context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED)
                && manager.areNotificationsEnabled() && channel != null && channel.getImportance() != NotificationManager.IMPORTANCE_NONE;
    }

    static void clear(Context context) { context.getSystemService(NotificationManager.class).cancelAll(); }

    static void display(Context context, JSONObject delivery) throws JSONException {
        if (PushWork.locallyBlocked || !permitted(context)) {
            new PushStore(context).deliveryStage(PushWork.locallyBlocked ? PushStore.DeliveryStage.LOCALLY_BLOCKED
                    : PushStore.DeliveryStage.PERMISSION_DENIED);
            return;
        }
        String url = PushContract.incidentUrl(delivery);
        Intent intent = new Intent(context, MainActivity.class).setAction(Intent.ACTION_VIEW).setData(Uri.parse(url))
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TOP);
        PendingIntent click = PendingIntent.getActivity(context, 0, intent, PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        Notification publicVersion = new Notification.Builder(context, CHANNEL).setSmallIcon(R.drawable.ic_notification)
                .setContentTitle("Heartbeat Checker").setContentText("Новое событие мониторинга").build();
        Notification notification = new Notification.Builder(context, CHANNEL).setSmallIcon(R.drawable.ic_notification)
                .setContentTitle(delivery.getString("title")).setContentText(delivery.getString("body"))
                .setStyle(new Notification.BigTextStyle().bigText(delivery.getString("body")))
                .setCategory(Notification.CATEGORY_STATUS).setColor(Color.rgb(0, 127, 115))
                .setVisibility(Notification.VISIBILITY_PRIVATE).setPublicVersion(publicVersion)
                .setContentIntent(click).setAutoCancel(true).build();
        context.getSystemService(NotificationManager.class).notify("push:" + delivery.getString("monitorId"), 0, notification);
        new PushStore(context).deliveryStage(PushStore.DeliveryStage.DISPLAY_SUBMITTED);
    }
}
