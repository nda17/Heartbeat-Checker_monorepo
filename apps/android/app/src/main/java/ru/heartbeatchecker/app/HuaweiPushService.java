package ru.heartbeatchecker.app;

import android.app.Application;
import android.os.Bundle;
import com.huawei.hms.push.HmsMessageService;
import com.huawei.hms.push.RemoteMessage;

public final class HuaweiPushService extends HmsMessageService {
    @Override public void onNewToken(String token) {
        if (acceptsCallbacks()) PushSdk.rotated(this, "huawei", token);
    }
    @Override public void onNewToken(String token, Bundle bundle) {
        // Multi-sender tokens belong to another routing scope and are never used.
        if (acceptsCallbacks() && (bundle == null || bundle.getString(SUBJECT_ID, "").isEmpty())) PushSdk.rotated(this, "huawei", token);
    }
    @Override public void onMessageReceived(RemoteMessage message) {
        if (!acceptsCallbacks()) return;
        PushReceiver.receive(this, message == null || !empty(message.getNotification()) ? null : message.getDataOfMap());
    }
    private boolean acceptsCallbacks() {
        return acceptsCallbacks(Application.getProcessName(), getPackageName(), PushSdk.configured("huawei"));
    }
    static boolean acceptsCallbacks(String processName, String packageName, boolean configured) {
        return configured && HeartbeatApplication.isMainProcess(processName, packageName);
    }
    private static boolean absent(String value) { return value == null || value.isEmpty(); }
    static boolean empty(RemoteMessage.Notification value) {
        // HMS always constructs an empty Notification, even for data-only messages.
        return value == null || (absent(value.getTitle()) && absent(value.getBody())
                && absent(value.getTitleLocalizationKey()) && absent(value.getBodyLocalizationKey())
                && (value.getTitleLocalizationArgs() == null || value.getTitleLocalizationArgs().length == 0)
                && (value.getBodyLocalizationArgs() == null || value.getBodyLocalizationArgs().length == 0)
                && absent(value.getIcon()) && value.getImageUrl() == null && absent(value.getSound())
                && absent(value.getTag()) && absent(value.getColor()) && absent(value.getClickAction())
                && absent(value.getIntentUri()) && absent(value.getChannelId()) && value.getLink() == null
                && absent(value.getTicker()));
    }
}
