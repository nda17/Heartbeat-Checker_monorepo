package ru.heartbeatchecker.app;

import android.app.Application;

public final class HeartbeatApplication extends Application {
    @Override public void onCreate() {
        super.onCreate();
        try { PushNotifications.channel(this); }
        catch (RuntimeException ignored) { /* The cabinet remains available without notifications. */ }
        // RuStore 7.5 permits background initialization started from Application.onCreate.
        PushWork.IO.execute(() -> PushSdk.initialize(this));
        PushWork.resume(this);
    }
}
