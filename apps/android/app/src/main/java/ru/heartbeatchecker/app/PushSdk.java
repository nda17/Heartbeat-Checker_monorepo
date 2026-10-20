package ru.heartbeatchecker.app;

import android.app.Application;
import android.content.Context;

import java.io.IOException;

import ru.rustore.sdk.pushclient.RuStorePushClient;
import ru.rustore.sdk.pushclient.common.logger.Logger;
import ru.rustore.sdk.core.feature.model.FeatureAvailabilityResult;

final class PushSdk {
    interface TokenResult { void ready(String token); void unavailable(); }
    private PushSdk() {}

    static void initialize(Application application) {
        if (BuildConfig.RUSTORE_PUSH_PROJECT_ID.isEmpty()) return;
        try {
            RuStorePushClient.INSTANCE.init(application, BuildConfig.RUSTORE_PUSH_PROJECT_ID, new QuietLogger());
        } catch (RuntimeException ignored) {
            // Missing distributor/configuration must not prevent cabinet startup.
        }
    }

    static void token(Context context, TokenResult result) {
        try {
            PushStore store = new PushStore(context);
            long revision = store.read().optLong("tokenRevision");
            if (BuildConfig.RUSTORE_PUSH_PROJECT_ID.isEmpty() || !RuStorePushClient.INSTANCE.isInitialized()) {
                result.unavailable(); return;
            }
            RuStorePushClient.INSTANCE.checkPushAvailability().addOnSuccessListener(availability -> {
                if (!(availability instanceof FeatureAvailabilityResult.Available)) { result.unavailable(); return; }
                fetchToken(store, revision, result);
            })
                    .addOnFailureListener(ignored -> result.unavailable());
        } catch (RuntimeException | IOException ignored) { result.unavailable(); }
    }

    private static void fetchToken(PushStore store, long revision, TokenResult result) {
        try {
            RuStorePushClient.INSTANCE.getToken().addOnSuccessListener(token -> {
                try {
                    String current;
                    synchronized (PushStore.LOCK) {
                        org.json.JSONObject latest = store.read();
                        current = latest.optLong("tokenRevision") != revision ? latest.optString("token") : token;
                        if (!PushContract.token(current)) { result.unavailable(); return; }
                        store.update(state -> {
                            if (!current.equals(state.optString("token")))
                                state.put("token", current).put("tokenRevision", state.optLong("tokenRevision") + 1);
                        });
                    }
                    result.ready(current);
                } catch (IOException ignored) { result.unavailable(); }
            }).addOnFailureListener(ignored -> result.unavailable());
        } catch (RuntimeException ignored) { result.unavailable(); }
    }

    static void rotated(Context context, String token) {
        if (!PushContract.token(token)) return;
        try {
            PushStore store = new PushStore(context);
            store.update(state -> {
                if (!token.equals(state.optString("token")))
                    state.put("token", token).put("tokenRevision", state.optLong("tokenRevision") + 1);
            });
            if (store.read().optBoolean("enabled")) PushWork.registration(context);
        } catch (IOException | RuntimeException ignored) { /* Fail closed; never expose a token in logs. */ }
    }

    private static final class QuietLogger implements Logger {
        @Override public void verbose(String message, Throwable error) { }
        @Override public void debug(String message, Throwable error) { }
        @Override public void info(String message, Throwable error) { }
        @Override public void warn(String message, Throwable error) { }
        @Override public void error(String message, Throwable error) { }
        @Override public Logger createLogger(String tag) { return this; }
        @Override public Logger createLogger(Object tag) { return this; }
    }
}
