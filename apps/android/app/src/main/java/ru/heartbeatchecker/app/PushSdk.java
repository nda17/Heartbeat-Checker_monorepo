package ru.heartbeatchecker.app;

import android.app.Application;
import android.content.Context;

import com.google.android.gms.common.GoogleApiAvailability;
import com.google.firebase.FirebaseApp;
import com.google.firebase.FirebaseOptions;
import com.google.firebase.installations.FirebaseInstallations;
import com.google.firebase.messaging.FirebaseMessaging;
import org.json.JSONObject;

import java.io.IOException;
import ru.rustore.sdk.pushclient.RuStorePushClient;
import ru.rustore.sdk.pushclient.common.logger.Logger;
import ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException;
import ru.rustore.sdk.core.feature.model.FeatureAvailabilityResult;

/** One active route per installation. Other SDK callbacks only cache candidates. */
final class PushSdk {
    interface TokenResult { void ready(String token); void unavailable(); }
    private static volatile boolean initialized;
    static volatile boolean bindingInProgress;
    private PushSdk() {}

    static void initialize(Application application) {
        try {
            if (!BuildConfig.RUSTORE_PUSH_PROJECT_ID.isEmpty())
                RuStorePushClient.INSTANCE.init(application, BuildConfig.RUSTORE_PUSH_PROJECT_ID, new QuietLogger());
        } catch (RuntimeException ignored) { /* Another configured provider may be available. */ }
        try {
            if (configured("fcm")) {
                if (FirebaseApp.getApps(application).isEmpty()) FirebaseApp.initializeApp(application, new FirebaseOptions.Builder()
                        .setApplicationId(BuildConfig.FIREBASE_APP_ID).setApiKey(BuildConfig.FIREBASE_API_KEY)
                        .setProjectId(BuildConfig.FIREBASE_PROJECT_ID).setGcmSenderId(BuildConfig.FIREBASE_SENDER_ID).build());
                FirebaseMessaging.getInstance().setAutoInitEnabled(false);
            }
        } catch (RuntimeException ignored) { /* Missing/invalid configuration fails this provider closed. */ }

        initialized = true;
    }

    static boolean configured(String provider) {
        switch (provider) {
            case "fcm": return !BuildConfig.FIREBASE_APP_ID.isEmpty() && !BuildConfig.FIREBASE_API_KEY.isEmpty()
                    && !BuildConfig.FIREBASE_PROJECT_ID.isEmpty() && !BuildConfig.FIREBASE_SENDER_ID.isEmpty();
            case "rustore": return !BuildConfig.RUSTORE_PUSH_PROJECT_ID.isEmpty();
            default: return false;
        }
    }

    static String provider(JSONObject state) {
        String selected = state.optString("provider");
        JSONObject installation = state.optJSONObject("installation");
        if (!PushContract.provider(selected) && installation != null && installation.optLong("bindingGeneration") > 0) selected = installation.optString("provider");
        // Upgrade from 1.1.3 keeps its existing RuStore token and route.
        if (!PushContract.provider(selected) && PushContract.token(state.optString("token"))) selected = "rustore";
        return selected;
    }

    private static boolean supported(Context context, String provider) {
        if (!configured(provider)) return false;
        try {
            if ("fcm".equals(provider)) return GoogleApiAvailability.getInstance().isGooglePlayServicesAvailable(context) == 0;
            return RuStorePushClient.INSTANCE.isInitialized();
        } catch (RuntimeException ignored) { return false; }
    }

    private static boolean definitivelyUnavailable(Context context, String provider) {
        if (!configured(provider)) return true;
        try {
            if ("fcm".equals(provider)) {
                int status = GoogleApiAvailability.getInstance().isGooglePlayServicesAvailable(context);
                return status == 1 || status == 9;
            }
            return false; // RuStore owns distributor detection, including non-store hosts.
        } catch (RuntimeException ignored) { return false; }
    }

    static void token(Context context, TokenResult result) {
        if (!initialized) { result.unavailable(); return; }
        try {
            PushStore store = new PushStore(context);
            JSONObject state = store.read();
            if ((PushWork.locallyBlocked || state.optBoolean("optedOut")) && !bindingInProgress) { result.unavailable(); return; }
            JSONObject capabilities = new PushApi().request("GET", "/api/v1/push/capabilities", "", null);
            if (capabilities.optInt("protocolVersion") != 2) { result.unavailable(); return; }
            store.update(value -> value.put("serverProviders", capabilities.getJSONArray("providers")));
            String current = provider(state);
            boolean allowed = false;
            org.json.JSONArray available = capabilities.getJSONArray("providers");
            for (int i=0; i<available.length(); i++) allowed |= current.equals(available.optString(i));
            if (PushContract.provider(current) && allowed && !definitivelyUnavailable(context, current)) {
                request(context, store, current, result); return;
            }
            choose(context, store, current, 0, result);
        } catch (IOException | org.json.JSONException | RuntimeException ignored) { result.unavailable(); }
    }

    private static void choose(Context context, PushStore store, String previous, int index, TokenResult result) {
        String[] order = {"fcm", "rustore"};
        if (index >= order.length) { store.providerIssue(PushStore.ProviderIssue.SDK_UNAVAILABLE); result.unavailable(); return; }
        String target = order[index];
        boolean allowed = false;
        try {
            org.json.JSONArray available = store.read().optJSONArray("serverProviders");
            if (available != null) for (int i=0; i<available.length(); i++) allowed |= target.equals(available.optString(i));
        } catch (IOException ignored) { result.unavailable(); return; }
        if (target.equals(previous) || !allowed || !supported(context, target)) { choose(context, store, previous, index + 1, result); return; }
        if ("rustore".equals(target)) {
            try {
                RuStorePushClient.INSTANCE.checkPushAvailability().addOnSuccessListener(available -> {
                    if (available instanceof FeatureAvailabilityResult.Available) select(context, store, previous, target, result);
                    else choose(context, store, previous, index + 1, result);
                }).addOnFailureListener(ignored -> result.unavailable());
            } catch (RuntimeException ignored) { result.unavailable(); }
        } else select(context, store, previous, target, result);
    }

    private static void select(Context context, PushStore store, String previous, String target, TokenResult result) {
        try {
            synchronized (PushStore.LOCK) {
                JSONObject latest = store.read();
                if (!previous.equals(provider(latest)) || (PushWork.locallyBlocked || latest.optBoolean("optedOut")) && !bindingInProgress) { result.unavailable(); return; }
                store.update(state -> {
                    state.put("provider", target); state.remove("token");
                    state.remove("providerDiagnostic");
                    state.remove("registeredToken"); state.remove("registeredProvider");
                    state.put("tokenRevision", state.optLong("tokenRevision") + 1);
                });
            }
            request(context, store, target, result);
        } catch (IOException ignored) { result.unavailable(); }
    }

    private static void request(Context context, PushStore store, String provider, TokenResult result) {
        try {
            long revision = store.read().optLong("tokenRevision");
            if ("fcm".equals(provider)) {
                FirebaseMessaging.getInstance().register().addOnSuccessListener(unused ->
                        FirebaseInstallations.getInstance().getId().addOnSuccessListener(value -> acquired(context, store, provider, value, revision, result))
                                .addOnFailureListener(ignored -> result.unavailable()))
                        .addOnFailureListener(ignored -> result.unavailable());
            } else if ("rustore".equals(provider)) {
                RuStorePushClient.INSTANCE.checkPushAvailability().addOnSuccessListener(available -> {
                    if (!(available instanceof FeatureAvailabilityResult.Available)) {
                        if (available instanceof FeatureAvailabilityResult.Unavailable
                                && ((FeatureAvailabilityResult.Unavailable) available).getCause() instanceof RuStorePushClientException.HostAppNotInstalledException)
                            choose(context, store, provider, 0, result);
                        else result.unavailable();
                        return;
                    }
                    RuStorePushClient.INSTANCE.getToken().addOnSuccessListener(value -> acquired(context, store, provider, value, revision, result))
                            .addOnFailureListener(ignored -> result.unavailable());
                }).addOnFailureListener(ignored -> result.unavailable());
            } else result.unavailable();
        } catch (IOException | RuntimeException ignored) { result.unavailable(); }
    }

    private static void acquired(Context context, PushStore store, String provider, String token, long revision, TokenResult result) {
        try {
            synchronized (PushStore.LOCK) {
                JSONObject latest = store.read();
                if (!provider.equals(provider(latest)) || (PushWork.locallyBlocked || latest.optBoolean("optedOut")) && !bindingInProgress) { result.unavailable(); return; }
                String current = latest.optLong("tokenRevision") != revision ? latest.optString("token") : token;
                if (!PushContract.token(current)) { result.unavailable(); return; }
                store.update(state -> {
                    state.put("provider", provider);
                    if (!current.equals(state.optString("token"))) state.put("token", current).put("tokenRevision", state.optLong("tokenRevision") + 1);
                });
                result.ready(current);
            }
        } catch (IOException ignored) { result.unavailable(); }
    }

    static void rotated(Context context, String token) { rotated(context, "rustore", token); }

    static void rotated(Context context, String provider, String token) {
        if (!PushContract.provider(provider) || !PushContract.token(token)) return;
        try {
            PushStore store = new PushStore(context);
            store.update(state -> {
                state.put("candidate_" + provider, token);
                // Late callbacks from an unselected SDK cannot take over a binding.
                if (provider.equals(provider(state)) && !state.optBoolean("optedOut") && !PushWork.locallyBlocked
                        && !token.equals(state.optString("token")))
                    state.put("token", token).put("tokenRevision", state.optLong("tokenRevision") + 1);
            });
            if (provider.equals(provider(store.read())) && store.read().optBoolean("enabled")) PushWork.registration(context);
        } catch (IOException | RuntimeException ignored) { /* Never expose SDK identifiers in logs. */ }
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
