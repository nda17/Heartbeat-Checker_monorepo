package ru.heartbeatchecker.app;

import java.util.List;

import ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException;
import ru.rustore.sdk.pushclient.messaging.model.RemoteMessage;
import ru.rustore.sdk.pushclient.messaging.service.RuStoreMessagingService;

public final class HeartbeatMessagingService extends RuStoreMessagingService {
    @Override public void onNewToken(String token) { PushSdk.rotated(this, token); }

    @Override public void onMessageReceived(RemoteMessage message) {
        // The server uses data-only messages. No payload-derived URL or content is displayed.
        if (message == null || message.getNotification() != null) return;
        try { PushWork.delivery(this, PushContract.envelope(message.getData(), System.currentTimeMillis())); }
        catch (Exception ignored) { /* Untrusted or stale messages are discarded without logging. */ }
    }

    @Override public void onDeletedMessages() { /* The cabinet remains the history authority. */ }
    @Override public void onError(List<? extends RuStorePushClientException> errors) {
        // SDK exceptions and RemoteMessage.from may contain provider credentials.
    }
}
