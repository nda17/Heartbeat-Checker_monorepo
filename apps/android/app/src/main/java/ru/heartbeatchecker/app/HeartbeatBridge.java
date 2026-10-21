package ru.heartbeatchecker.app;

import android.app.Activity;
import android.content.Intent;
import android.net.Uri;
import android.util.Base64;

import androidx.browser.customtabs.CustomTabsIntent;

import com.facebook.react.bridge.Arguments;
import com.facebook.react.bridge.Promise;
import com.facebook.react.bridge.ReactApplicationContext;
import com.facebook.react.bridge.ReactContextBaseJavaModule;
import com.facebook.react.bridge.ReactMethod;
import com.facebook.react.bridge.ReadableMap;
import com.facebook.react.bridge.UiThreadUtil;
import com.facebook.react.bridge.WritableArray;
import com.facebook.react.bridge.WritableMap;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.Iterator;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/** Narrow Java bridge for encrypted state, exact App Links, system browser and push. */
public final class HeartbeatBridge extends ReactContextBaseJavaModule {
    private final ReactApplicationContext context;
    private final ExecutorService io = Executors.newSingleThreadExecutor();
    private final ExecutorService pushIo = Executors.newSingleThreadExecutor();
    private final SecureRandom random = new SecureRandom();
    private final YooCheckout checkout;

    HeartbeatBridge(ReactApplicationContext context) {
        super(context); this.context = context; this.checkout = new YooCheckout(context);
    }
    @Override public String getName() { return "HeartbeatBridge"; }
    @Override public Map<String, Object> getConstants() {
        Map<String, Object> constants = new java.util.HashMap<>();
        constants.put("API_ORIGIN", BuildConfig.API_ORIGIN);
        constants.put("CHALLENGE_ORIGIN", BuildConfig.CHALLENGE_ORIGIN);
        return constants;
    }

    private interface Action { Object run() throws Exception; }
    private void work(Promise promise, Action action) {
        io.execute(() -> {
            try {
                Object value = action.run();
                if (value instanceof JSONObject) promise.resolve(map((JSONObject) value));
                else promise.resolve(value);
            } catch (PushNative.TransferRequired transfer) {promise.reject("push_account_change_required", "Confirm transfer of this installation");}
            catch (Exception failure) { promise.reject("native_operation_failed", "Native operation could not complete"); }
        });
    }

    private static WritableMap map(JSONObject value) throws JSONException {
        WritableMap result = Arguments.createMap();
        for (Iterator<String> keys = value.keys(); keys.hasNext();) {
            String key = keys.next(); Object item = value.get(key);
            if (item == JSONObject.NULL) result.putNull(key);
            else if (item instanceof String) result.putString(key, (String) item);
            else if (item instanceof Boolean) result.putBoolean(key, (Boolean) item);
            else if (item instanceof Integer) result.putInt(key, (Integer) item);
            else if (item instanceof Number) result.putDouble(key, ((Number) item).doubleValue());
            else if (item instanceof JSONObject) result.putMap(key, map((JSONObject) item));
            else if (item instanceof JSONArray) result.putArray(key, array((JSONArray) item));
        }
        return result;
    }
    private static WritableArray array(JSONArray value) throws JSONException {
        WritableArray result = Arguments.createArray();
        for (int i = 0; i < value.length(); i++) {
            Object item = value.get(i);
            if (item == JSONObject.NULL) result.pushNull();
            else if (item instanceof String) result.pushString((String) item);
            else if (item instanceof Boolean) result.pushBoolean((Boolean) item);
            else if (item instanceof Integer) result.pushInt((Integer) item);
            else if (item instanceof Number) result.pushDouble(((Number) item).doubleValue());
            else if (item instanceof JSONObject) result.pushMap(map((JSONObject) item));
            else if (item instanceof JSONArray) result.pushArray(array((JSONArray) item));
        }
        return result;
    }
    private static JSONObject json(ReadableMap value) { return new JSONObject(value.toHashMap()); }
    private String secret() {
        byte[] bytes = new byte[32]; random.nextBytes(bytes);
        return Base64.encodeToString(bytes, Base64.URL_SAFE | Base64.NO_WRAP | Base64.NO_PADDING);
    }

    @ReactMethod public void restore(Promise promise) { work(promise, () -> new AuthStore(context).active()); }
    @ReactMethod public void authEpoch(Promise promise) {work(promise, () -> new AuthStore(context).epoch());}
    @ReactMethod public void saveSession(ReadableMap value, double expectedEpoch, Promise promise) {
        work(promise, () -> {
            if (expectedEpoch < 0 || expectedEpoch != Math.floor(expectedEpoch)) throw new IllegalArgumentException();
            new AuthStore(context).saveSession(json(value), (long) expectedEpoch); return null;
        });
    }
    @ReactMethod public void logout(Promise promise) {
        checkout.invalidate();
        work(promise, () -> {
            new AuthStore(context).logout();
            PushWork.blockLocally(context);
            try {PushWork.logout(context);} catch (Exception ignored) { /* The auth gate remains closed. */ }
            try {AuthRevokeWorker.enqueue(context);} catch (Exception ignored) { /* The queue is recovered at next launch. */ }
            return null;
        });
    }
    @ReactMethod public void randomUUID(Promise promise) { work(promise, () -> UUID.randomUUID().toString()); }
    @ReactMethod public void randomToken(Promise promise) { work(promise, this::secret); }
    @ReactMethod public void sdkCapabilities(Promise promise) {work(promise, checkout::capabilities);}
    @ReactMethod public void sdkTokenize(ReadableMap input, Promise promise) {checkout.tokenize(input, promise);}
    @ReactMethod public void sdkConfirm(ReadableMap input, Promise promise) {checkout.confirm(input, promise);}

    @ReactMethod public void beginOAuth(Promise promise) {
        work(promise, () -> {
            String state = secret(), verifier = secret();
            byte[] digest = MessageDigest.getInstance("SHA-256").digest(verifier.getBytes(java.nio.charset.StandardCharsets.US_ASCII));
            String challenge = Base64.encodeToString(digest, Base64.URL_SAFE | Base64.NO_WRAP | Base64.NO_PADDING);
            AuthStore store = new AuthStore(context);
            JSONObject active = store.active();
            JSONObject pending = new JSONObject().put("state", state).put("codeVerifier", verifier)
                    .put("createdAt", System.currentTimeMillis()).put("authEpoch", store.epoch());
            if (active != null) pending.put("sessionId", active.getJSONObject("session").getString("id"));
            store.put("oauth", pending);
            return new JSONObject().put("state", state).put("codeChallenge", challenge);
        });
    }
    @ReactMethod public void saveOAuth(ReadableMap value, Promise promise) {
        work(promise, () -> {
            AuthStore store = new AuthStore(context); JSONObject existing = store.read().optJSONObject("oauth"), input = json(value);
            if (existing == null || !existing.optString("state").equals(input.optString("state"))
                    || System.currentTimeMillis() - existing.optLong("createdAt") > 10 * 60_000L
                    || store.epoch() != existing.optLong("authEpoch")
                    || !PushContract.uuid(input.optString("flowId"))
                    || !"yandex".equals(input.optString("provider")) && !"vk".equals(input.optString("provider"))
                    || !"login".equals(input.optString("mode")) && !"link".equals(input.optString("mode"))
                    || ("link".equals(input.optString("mode")) != (store.active() != null))
                    || ("link".equals(input.optString("mode")) && !existing.optString("sessionId").equals(
                            store.active().getJSONObject("session").getString("id")))
                    || PushContract.instant(input.optString("expiresAt")) <= System.currentTimeMillis()) throw new IllegalArgumentException();
            existing.put("flowId", input.getString("flowId")).put("provider", input.getString("provider"))
                    .put("mode", input.getString("mode")).put("expiresAt", input.getString("expiresAt"));
            store.put("oauth", existing); return null;
        });
    }
    @ReactMethod public void takeOAuth(String flowId, String state, Promise promise) {
        work(promise, () -> {
            AuthStore store = new AuthStore(context);
            JSONObject pending = store.read().optJSONObject("oauth");
            if (pending == null || !flowId.equals(pending.optString("flowId")) || !state.equals(pending.optString("state"))
                    || PushContract.instant(pending.optString("expiresAt")) <= System.currentTimeMillis()
                    || store.epoch() != pending.optLong("authEpoch")
                    || ("link".equals(pending.optString("mode")) && (store.active() == null || !pending.optString("sessionId").equals(
                            store.active().getJSONObject("session").getString("id"))))) throw new IllegalArgumentException();
            pending.remove("createdAt"); return pending;
        });
    }
    @ReactMethod public void clearOAuth(String flowId, String state, Promise promise) {
        work(promise, () -> {new AuthStore(context).removeOAuth(flowId, state); return null;});
    }
    @ReactMethod public void saveOAuthRegistration(ReadableMap value, Promise promise) {
        work(promise, () -> {
            JSONObject input = json(value);
            if (input.optString("registrationToken").length() < 32 || PushContract.instant(input.optString("expiresAt")) <= System.currentTimeMillis()) throw new IllegalArgumentException();
            new AuthStore(context).put("oauthRegistration", input); return null;
        });
    }
    @ReactMethod public void restoreOAuthRegistration(Promise promise) {
        work(promise, () -> {
            JSONObject value = new AuthStore(context).read().optJSONObject("oauthRegistration");
            return value != null && PushContract.instant(value.optString("expiresAt")) > System.currentTimeMillis() ? value : null;
        });
    }
    @ReactMethod public void clearOAuthRegistration(Promise promise) {work(promise, () -> {new AuthStore(context).remove("oauthRegistration"); return null;});}

    @ReactMethod public void saveRegistration(ReadableMap value, Promise promise) {
        work(promise, () -> {
            JSONObject input = json(value), challenge = input.getJSONObject("challenge");
            if (input.optString("registrationToken").length() < 32
                    || !PushContract.uuid(challenge.optString("id"))
                    || PushContract.instant(challenge.optString("expiresAt")) <= System.currentTimeMillis()) throw new IllegalArgumentException();
            new AuthStore(context).put("registration", input); return null;
        });
    }
    @ReactMethod public void restoreRegistration(Promise promise) {
        work(promise, () -> {
            JSONObject value = new AuthStore(context).read().optJSONObject("registration");
            JSONObject challenge = value == null ? null : value.optJSONObject("challenge");
            return challenge != null && PushContract.instant(challenge.optString("expiresAt")) > System.currentTimeMillis() ? value : null;
        });
    }
    @ReactMethod public void clearRegistration(Promise promise) {work(promise, () -> {new AuthStore(context).remove("registration"); return null;});}

    @ReactMethod public void savePaymentIntent(ReadableMap value, Promise promise) {
        work(promise, () -> {
            AuthStore store = new AuthStore(context); JSONObject active = store.active(), input = json(value);
            if (active == null || !active.getJSONObject("account").getString("id").equals(input.optString("accountId"))
                    || !active.getJSONObject("session").getString("id").equals(input.optString("sessionId"))
                    || !PushContract.uuid(input.optString("idempotencyKey"))
                    || !validPaymentIntent(input)) throw new IllegalArgumentException();
            JSONObject prior = store.read().optJSONObject("paymentIntent");
            if (prior != null && !prior.optString("idempotencyKey").equals(input.optString("idempotencyKey"))) throw new IllegalStateException();
            store.put("paymentIntent", input); return null;
        });
    }
    @ReactMethod public void restorePaymentIntent(Promise promise) {
        work(promise, () -> {
            AuthStore store = new AuthStore(context); JSONObject active = store.active(), intent = store.read().optJSONObject("paymentIntent");
            if (active == null || intent == null) return null;
            if (!validPaymentIntent(intent)) throw new IllegalArgumentException();
            return active.getJSONObject("account").getString("id").equals(intent.optString("accountId"))
                    && active.getJSONObject("session").getString("id").equals(intent.optString("sessionId")) ? intent : null;
        });
    }

    private static boolean validPaymentIntent(JSONObject value) {
        try {
            String kind = value.getString("checkoutKind"), purchase = value.getString("purchaseKind");
            String packageId = value.getString("packageId"), orderId = value.optString("orderId");
            long createdAt = PushContract.instant(value.getString("createdAt")), now = System.currentTimeMillis();
            return ("android_sdk".equals(kind) || "redirect".equals(kind))
                    && ("add".equals(purchase) || "renew".equals(purchase))
                    && ("add".equals(purchase) ? packageId.isEmpty() : PushContract.uuid(packageId))
                    && (!value.has("orderId") || PushContract.uuid(orderId))
                    && value.getInt("monitorCount") >= 1 && value.getInt("monitorCount") <= 1000
                    && value.getInt("priceVersion") >= 1 && createdAt <= now + 5 * 60_000L;
        } catch (Exception ignored) {return false;}
    }
    @ReactMethod public void clearPaymentIntent(Promise promise) {
        work(promise, () -> {
            AuthStore store = new AuthStore(context); JSONObject active = store.active(), intent = store.read().optJSONObject("paymentIntent");
            if (active != null && intent != null && active.getJSONObject("account").getString("id").equals(intent.optString("accountId"))
                    && active.getJSONObject("session").getString("id").equals(intent.optString("sessionId"))) store.remove("paymentIntent");
            return null;
        });
    }

    @ReactMethod public void takeInitialLink(Promise promise) {
        UiThreadUtil.runOnUiThread(() -> {
            Activity activity = getCurrentActivity();
            Intent intent = activity == null ? null : activity.getIntent();
            String link = MainActivity.normalize(intent);
            if (intent != null && link != null) intent.setData(null);
            promise.resolve(link);
        });
    }
    @ReactMethod public void openCustomTab(String url, Promise promise) {
        UiThreadUtil.runOnUiThread(() -> {
            Activity activity = getCurrentActivity();
            try {
                Uri uri = Uri.parse(url);
                String host = uri.getHost(); String path = uri.getPath();
                boolean own = "heartbeat-checker.ru".equals(host) && (path.equals("/contacts") || path.equals("/terms")
                        || path.equals("/privacy") || path.equals("/cookies"));
                boolean oauth = "oauth.yandex.ru".equals(host) && path.equals("/authorize") || "id.vk.ru".equals(host) && path.equals("/authorize");
                boolean payment = "yoomoney.ru".equals(host) || "yookassa.ru".equals(host);
                boolean bot = "t.me".equals(host) || "max.ru".equals(host);
                if (activity == null || !"https".equals(uri.getScheme()) || uri.getPort() != -1 || uri.getUserInfo() != null
                        || uri.getFragment() != null || url.length() > 2048 || !(own || oauth || payment || bot)) throw new IllegalArgumentException();
                new CustomTabsIntent.Builder().build().launchUrl(activity, uri);
                promise.resolve(null);
            } catch (Exception failure) {promise.reject("browser_unavailable", "Cannot open the external page");}
        });
    }

    @ReactMethod public void pushStatus(Promise promise) {
        work(promise, () -> {
            if ("test".equals(BuildConfig.BILLING_ENVIRONMENT))
                return new JSONObject().put("enabled", false).put("optedOut", true).put("provider", "").put("status", "not_connected");
            JSONObject state = new PushStore(context).read();
            JSONObject installation = state.optJSONObject("installation");
            return new JSONObject().put("enabled", state.optBoolean("enabled") && !PushWork.locallyBlocked)
                    .put("optedOut", state.optBoolean("optedOut"))
                    .put("provider", PushSdk.provider(state)).put("status", installation == null ? "not_connected" : installation.optString("status"));
        });
    }
    @ReactMethod public void pushToggle(boolean enabled, boolean allowAccountChange, Promise promise) {
        if ("test".equals(BuildConfig.BILLING_ENVIRONMENT)) {promise.reject("push_unavailable", "Push is unavailable in isolated SDK testing"); return;}
        pushIo.execute(() -> {
            try {PushNative.toggle(context, enabled, allowAccountChange); promise.resolve(null);}
            catch (PushNative.TransferRequired transfer) {promise.reject("push_account_change_required", "Confirm transfer of this installation");}
            catch (Exception failure) {promise.reject("push_unavailable", "Push connection could not complete");}
        });
    }
}
