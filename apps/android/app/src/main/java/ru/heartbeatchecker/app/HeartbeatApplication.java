package ru.heartbeatchecker.app;

import android.app.Application;

public final class HeartbeatApplication extends Application {
    @Override public void onCreate() {
        super.onCreate();
        if (!isMainProcess(Application.getProcessName(), getPackageName())) {
            // HMS owns :pushservice. Only its public identity is initialized there;
            // our encrypted state, callbacks and WorkManager remain in the main process.
            PushSdk.initializeHuawei(this);
            return;
        }
        try { PushNotifications.channel(this); }
        catch (RuntimeException ignored) { /* The cabinet remains available without notifications. */ }
        // RuStore 7.5 permits background initialization started from Application.onCreate.
        PushWork.IO.execute(() -> PushSdk.initialize(this));
        PushWork.resume(this);
    }
    static boolean isMainProcess(String processName, String packageName) {
        return packageName != null && packageName.equals(processName);
    }
}
