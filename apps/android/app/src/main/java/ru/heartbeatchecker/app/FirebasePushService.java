package ru.heartbeatchecker.app;

import com.google.firebase.messaging.FirebaseMessagingService;
import com.google.firebase.messaging.RemoteMessage;

public final class FirebasePushService extends FirebaseMessagingService {
    @Override public void onRegistered(String installationId) { PushSdk.rotated(this, "fcm", installationId); }
    @Override public void onMessageReceived(RemoteMessage message) {
        PushReceiver.receive(this, message == null || message.getNotification() != null ? null : message.getData());
    }
}
