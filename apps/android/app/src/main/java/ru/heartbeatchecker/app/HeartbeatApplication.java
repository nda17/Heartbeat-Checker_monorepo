package ru.heartbeatchecker.app;

import android.app.Application;

import com.facebook.react.PackageList;
import com.facebook.react.ReactApplication;
import com.facebook.react.ReactHost;
import com.facebook.react.ReactNativeApplicationEntryPoint;
import com.facebook.react.ReactNativeHost;
import com.facebook.react.ReactPackage;
import com.facebook.react.defaults.DefaultReactHost;
import com.facebook.react.defaults.DefaultReactNativeHost;

import java.util.List;

public final class HeartbeatApplication extends Application implements ReactApplication {
    private final ReactNativeHost host = new DefaultReactNativeHost(this) {
        @Override public boolean getUseDeveloperSupport() { return BuildConfig.DEBUG; }
        @Override protected String getJSMainModuleName() { return "index"; }
        @Override protected List<ReactPackage> getPackages() {
            List<ReactPackage> packages = new PackageList(this).getPackages();
            packages.add(new HeartbeatPackage());
            return packages;
        }
    };

    @Override public ReactNativeHost getReactNativeHost() { return host; }
    @Override public ReactHost getReactHost() { return DefaultReactHost.getDefaultReactHost(this, host, null); }

    @Override public void onCreate() {
        super.onCreate();
        if (!isMainProcess(Application.getProcessName(), getPackageName())) return;
        ReactNativeApplicationEntryPoint.loadReactNative(this);
        try { PushNotifications.channel(this); } catch (RuntimeException ignored) { }
        try {
            org.json.JSONObject push = new PushStore(this).read();
            PushWork.locallyBlocked = !push.optBoolean("enabled") || !new AuthStore(this).ownsPush(push);
        }
        catch (Exception ignored) { PushWork.locallyBlocked = true; }
        PushWork.IO.execute(() -> PushSdk.initialize(this));
        PushWork.resume(this);
        AuthRevokeWorker.enqueue(this);
    }

    static boolean isMainProcess(String processName, String packageName) {
        return packageName != null && packageName.equals(processName);
    }
}
