package ru.heartbeatchecker.app;

import android.app.Application;
import android.content.Context;

import com.google.android.gms.common.GoogleApiAvailability;
import com.google.firebase.FirebaseApp;
import com.google.firebase.FirebaseOptions;
import com.google.firebase.installations.FirebaseInstallations;
import com.google.firebase.messaging.FirebaseMessaging;
import com.huawei.agconnect.AGConnectInstance;
import com.huawei.agconnect.AGConnectOptionsBuilder;
import com.huawei.agconnect.config.AGConnectServicesConfig;
import com.huawei.hms.aaid.HmsInstanceId;
import com.huawei.hms.api.HuaweiApiAvailability;
import org.json.JSONObject;

import java.io.IOException;
import ru.rustore.sdk.pushclient.RuStorePushClient;
import ru.rustore.sdk.pushclient.common.logger.Logger;
import ru.rustore.sdk.core.feature.model.FeatureAvailabilityResult;

/** One active route per installation. Other SDK callbacks only cache candidates. */
final class PushSdk {
    interface TokenResult {
        void ready(String token);
        void unavailable();
        default boolean active() { return true; }
    }
    enum Availability { READY, MISSING, RETRY }
    private static volatile boolean initialized;
    // Binding may wait on PushWork.IO; token acquisition must not queue behind that wait.
    private static final java.util.concurrent.ExecutorService HUAWEI_IO = java.util.concurrent.Executors.newSingleThreadExecutor();
    static volatile boolean bindingInProgress;
    private PushSdk() {}

    static void initialize(Application application) {
        if (BuildConfig.DEV_CLIENT || BuildConfig.ISOLATED_API) return;
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

        initializeHuawei(application);
        initialized = true;
    }

    // HMS's :pushservice process receives public SDK identity only. No app state or workers.
    static void initializeHuawei(Context context) {
        if (!configured("huawei")) return;
        try {
            AGConnectServicesConfig config = AGConnectServicesConfig.fromContext(context);
            config.setParam("client/app_id", BuildConfig.HUAWEI_APP_ID);
            config.setParam("client/project_id", BuildConfig.HUAWEI_PROJECT_ID);
            config.setParam("client/cp_id", BuildConfig.HUAWEI_CP_ID);
            config.setParam("client/package_name", BuildConfig.APPLICATION_ID);
            AGConnectInstance.initialize(context, new AGConnectOptionsBuilder()
                    .setAppId(BuildConfig.HUAWEI_APP_ID).setCPId(BuildConfig.HUAWEI_CP_ID)
                    .setPackageName(BuildConfig.APPLICATION_ID).setCustomValue("client/project_id", BuildConfig.HUAWEI_PROJECT_ID));
        } catch (RuntimeException ignored) { /* A missing provider must not block the cabinet. */ }
    }

    static boolean configured(String provider) {
        switch (provider) {
            case "fcm": return !BuildConfig.FIREBASE_APP_ID.isEmpty() && !BuildConfig.FIREBASE_API_KEY.isEmpty()
                    && !BuildConfig.FIREBASE_PROJECT_ID.isEmpty() && !BuildConfig.FIREBASE_SENDER_ID.isEmpty();
            case "rustore": return !BuildConfig.RUSTORE_PUSH_PROJECT_ID.isEmpty();
            case "huawei": return BuildConfig.HUAWEI_PUSH_ENABLED && !BuildConfig.DEV_CLIENT && !BuildConfig.ISOLATED_API
                    && !BuildConfig.HUAWEI_APP_ID.isEmpty() && !BuildConfig.HUAWEI_PROJECT_ID.isEmpty() && !BuildConfig.HUAWEI_CP_ID.isEmpty();
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
            if ("huawei".equals(provider)) return HuaweiApiAvailability.getInstance().isHuaweiMobileServicesAvailable(context) == 0;
            return RuStorePushClient.INSTANCE.isInitialized();
        } catch (RuntimeException ignored) { return false; }
    }

    private static boolean definitivelyUnavailable(Context context, String provider) {
        if (!configured(provider)) return true;
        try {
            if ("fcm".equals(provider)) {
                int status = GoogleApiAvailability.getInstance().isGooglePlayServicesAvailable(context);
                return availability(provider, status) == Availability.MISSING;
            }
            if ("huawei".equals(provider)) return availability(provider,
                    HuaweiApiAvailability.getInstance().isHuaweiMobileServicesAvailable(context)) == Availability.MISSING;
            return false; // RuStore owns distributor detection, including non-store hosts.
        } catch (RuntimeException ignored) { return false; }
    }

    static Availability availability(String provider, int status) {
        if (status == 0) return Availability.READY;
        // HMS update/disabled/invalid/network states must not move an active binding.
        return status == 1 || "fcm".equals(provider) && status == 9 ? Availability.MISSING : Availability.RETRY;
    }

    static String[] providerOrder() { return new String[] {"fcm", "huawei", "rustore"}; }

    static int nextCandidate(String previous, org.json.JSONArray allowed, int start, java.util.function.Predicate<String> supported) {
        String[] order = providerOrder();
        for (int index = start; index < order.length; index++) {
            String target = order[index];
            boolean advertised = false;
            if (allowed != null) for (int i = 0; i < allowed.length(); i++) advertised |= target.equals(allowed.optString(i));
            if (!target.equals(previous) && advertised && supported.test(target)) return index;
        }
        return -1;
    }

    static int nextPreferredCandidate(String current, org.json.JSONArray allowed, int start, java.util.function.Predicate<String> supported) {
        int candidate = nextCandidate(current, allowed, start, supported);
        String[] order = providerOrder();
        boolean advertised = false;
        if (allowed != null) for (int i = 0; i < allowed.length(); i++) advertised |= current.equals(allowed.optString(i));
        if (advertised) for (int i = 0; i < order.length; i++)
            if (current.equals(order[i])) return candidate >= 0 && candidate < i ? candidate : -1;
        return -1;
    }

    static void requestBestProvider(String previous, org.json.JSONArray allowed, boolean keepCurrent,
            java.util.function.Predicate<String> supported, TokenResult result,
            java.util.function.BiConsumer<String, TokenResult> request) {
        if (!result.active()) { result.unavailable(); return; }
        int candidate = keepCurrent ? nextPreferredCandidate(previous, allowed, 0, supported)
                : nextCandidate(previous, allowed, 0, supported);
        String target = candidate >= 0 ? providerOrder()[candidate] : keepCurrent ? previous : "";
        if (!PushContract.provider(target) || !result.active()) { result.unavailable(); return; }
        // Select exactly one ready SDK. Token errors retry this route, never a lower-priority provider.
        request.accept(target, result);
    }

    static void requestTokenStage(java.util.function.BooleanSupplier active, TokenResult result, Runnable request) {
        if (!result.active() || !active.getAsBoolean()) { result.unavailable(); return; }
        request.run();
    }

    static boolean candidateMatches(JSONObject before, JSONObject after, long revocation, long currentRevocation, boolean active) {
        return candidateOwnerMatches(before, after, revocation, currentRevocation, active)
                && before.optLong("tokenRevision") == after.optLong("tokenRevision");
    }

    private static boolean candidateOwnerMatches(JSONObject before, JSONObject after, long revocation, long currentRevocation, boolean active) {
        return active && revocation == currentRevocation && PushNative.sameBinding(before, after)
                && provider(before).equals(provider(after)) && before.optString("accountId").equals(after.optString("accountId"))
                && before.optString("sessionId").equals(after.optString("sessionId"))
                && before.optBoolean("enabled") == after.optBoolean("enabled")
                && before.optBoolean("optedOut") == after.optBoolean("optedOut") && !after.has("pendingBind");
    }

    static void commitCandidate(JSONObject state, String target, String token) throws org.json.JSONException {
        if (!PushContract.provider(target) || !PushContract.token(token)) throw new org.json.JSONException("Push candidate unavailable");
        boolean changed = !target.equals(provider(state));
        if (changed || !token.equals(state.optString("token")))
            state.put("tokenRevision", state.optLong("tokenRevision") + 1);
        state.put("provider", target).put("token", token);
        if (changed) {
            state.remove("providerDiagnostic");
            state.remove("registeredToken"); state.remove("registeredProvider");
        }
    }

    private static final class TokenAttempt {
        final JSONObject before;
        final long revocation, authEpoch, expiresAt;
        final boolean binding;
        final TokenResult result;
        TokenAttempt(JSONObject before, long revocation, long authEpoch, boolean binding, TokenResult result) {
            this.before = before; this.revocation = revocation; this.authEpoch = authEpoch;
            this.binding = binding; this.result = result;
            expiresAt = System.nanoTime() + java.util.concurrent.TimeUnit.SECONDS.toNanos(30);
        }
        boolean active(Context context, JSONObject latest) throws IOException {
            AuthStore auth = new AuthStore(context);
            return result.active() && System.nanoTime() < expiresAt && binding == bindingInProgress
                    && (!PushWork.locallyBlocked && !latest.optBoolean("optedOut") || binding)
                    && auth.epoch() == authEpoch && auth.active() != null && (binding || auth.ownsPush(latest));
        }
        boolean current(Context context, PushStore store) {
            try {
                JSONObject latest = store.read();
                return candidateOwnerMatches(before, latest, revocation, PushWork.localRevocation, active(context, latest));
            } catch (IOException ignored) { return false; }
        }
    }

    static void token(Context context, TokenResult result) {
        if (BuildConfig.DEV_CLIENT || BuildConfig.ISOLATED_API) { result.unavailable(); return; }
        if (!initialized) { result.unavailable(); return; }
        try {
            PushStore store = new PushStore(context);
            JSONObject state = store.read();
            if ((PushWork.locallyBlocked || state.optBoolean("optedOut")) && !bindingInProgress) { result.unavailable(); return; }
            TokenAttempt attempt = new TokenAttempt(state, PushWork.localRevocation, new AuthStore(context).epoch(), bindingInProgress, result);
            if (state.has("pendingBind") || !attempt.active(context, state)) { result.unavailable(); return; }
            JSONObject capabilities = new PushApi().request("GET", "/api/v1/push/capabilities", "", null);
            if (capabilities.optInt("protocolVersion") != 2) { result.unavailable(); return; }
            store.update(value -> value.put("serverProviders", capabilities.getJSONArray("providers")));
            String current = provider(state);
            boolean allowed = false;
            org.json.JSONArray available = capabilities.getJSONArray("providers");
            for (int i=0; i<available.length(); i++) allowed |= current.equals(available.optString(i));
            if (PushContract.provider(current) && allowed && !definitivelyUnavailable(context, current)) {
                choose(context, store, attempt, available, true); return;
            }
            choose(context, store, attempt, available, false);
        } catch (IOException | org.json.JSONException | RuntimeException ignored) { result.unavailable(); }
    }

    private static void choose(Context context, PushStore store, TokenAttempt attempt, org.json.JSONArray available, boolean prefer) {
        String previous = provider(attempt.before);
        try {
            JSONObject latest = store.read();
            if (!candidateMatches(attempt.before, latest, attempt.revocation, PushWork.localRevocation, attempt.active(context, latest))) {
                attempt.result.unavailable(); return;
            }
        } catch (IOException ignored) { attempt.result.unavailable(); return; }
        requestBestProvider(previous, available, prefer, target -> supported(context, target), attempt.result,
                (target, result) -> request(context, store, attempt, target, result));
    }

    private static void request(Context context, PushStore store, TokenAttempt attempt, String provider, TokenResult result) {
        try {
            if (!attempt.current(context, store)) { result.unavailable(); return; }
            long revision = store.read().optLong("tokenRevision");
            if ("fcm".equals(provider)) {
                FirebaseMessaging.getInstance().register().addOnSuccessListener(unused ->
                        requestTokenStage(() -> attempt.current(context, store), result, () ->
                            FirebaseInstallations.getInstance().getId().addOnSuccessListener(value -> acquired(context, store, attempt, provider, value, revision, result))
                                    .addOnFailureListener(ignored -> result.unavailable())))
                        .addOnFailureListener(ignored -> result.unavailable());
            } else if ("huawei".equals(provider)) {
                HUAWEI_IO.execute(() -> {
                    try {
                        if (!attempt.current(context, store)) { result.unavailable(); return; }
                        String value = HmsInstanceId.getInstance(context).getToken(BuildConfig.HUAWEI_APP_ID, "HCM");
                        if (!PushContract.token(value)) value = store.read().optString("candidate_huawei");
                        acquired(context, store, attempt, provider, value, revision, result);
                    } catch (Exception ignored) { result.unavailable(); }
                });
            } else if ("rustore".equals(provider)) {
                RuStorePushClient.INSTANCE.checkPushAvailability().addOnSuccessListener(available -> {
                    if (!(available instanceof FeatureAvailabilityResult.Available)) {
                        result.unavailable();
                        return;
                    }
                    requestTokenStage(() -> attempt.current(context, store), result, () ->
                        RuStorePushClient.INSTANCE.getToken().addOnSuccessListener(value -> acquired(context, store, attempt, provider, value, revision, result))
                                .addOnFailureListener(ignored -> result.unavailable()));
                }).addOnFailureListener(ignored -> result.unavailable());
            } else result.unavailable();
        } catch (IOException | RuntimeException ignored) { result.unavailable(); }
    }

    private static void acquired(Context context, PushStore store, TokenAttempt attempt, String provider, String token, long revision, TokenResult result) {
        try {
            synchronized (PushStore.LOCK) {
                JSONObject latest = store.read();
                boolean sameProvider = provider.equals(provider(latest));
                boolean active = attempt.active(context, latest);
                if (!(sameProvider ? candidateOwnerMatches(attempt.before, latest, attempt.revocation, PushWork.localRevocation, active)
                        : candidateMatches(attempt.before, latest, attempt.revocation, PushWork.localRevocation, active))) {
                    result.unavailable(); return;
                }
                String current = sameProvider && latest.optLong("tokenRevision") != revision ? latest.optString("token") : token;
                if (!PushContract.token(current)) { result.unavailable(); return; }
                store.update(state -> commitCandidate(state, provider, current));
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
