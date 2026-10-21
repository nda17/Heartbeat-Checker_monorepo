package ru.heartbeatchecker.app;

import android.content.Context;

import androidx.annotation.NonNull;
import androidx.work.Worker;
import androidx.work.WorkerParameters;

import org.json.JSONArray;
import org.json.JSONObject;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.BooleanSupplier;
import java.util.function.Consumer;

public final class PushWorker extends Worker {
    public PushWorker(@NonNull Context context, @NonNull WorkerParameters parameters) { super(context, parameters); }

    @NonNull @Override public Result doWork() {
        if (BuildConfig.DEV_CLIENT) return Result.success();
        try {
            String kind = getInputData().getString("kind");
            if ("delivery".equals(kind)) return delivery();
            if ("registration".equals(kind)) return registration();
            if ("revoke".equals(kind)) return revoke();
        } catch (Exception ignored) {
            // Do not attach exceptions, payloads or credentials to WorkManager output/logs.
        }
        return Result.failure();
    }

    private Result retry() { return getRunAttemptCount() < 2 && !isStopped() ? Result.retry() : Result.failure(); }

    static boolean awaitToken(long timeoutMs, BooleanSupplier active, Consumer<PushSdk.TokenResult> request) throws InterruptedException {
        if (timeoutMs <= 0 || !active.getAsBoolean()) return false;
        CountDownLatch completed = new CountDownLatch(1); AtomicBoolean ready = new AtomicBoolean();
        request.accept(new PushSdk.TokenResult() {
            @Override public void ready(String token) { ready.set(PushContract.token(token)); completed.countDown(); }
            @Override public void unavailable() { completed.countDown(); }
        });
        return completed.await(timeoutMs, TimeUnit.MILLISECONDS) && active.getAsBoolean() && ready.get();
    }

    interface DeliveryServices {
        JSONObject fetch(String id, String credential) throws java.io.IOException, org.json.JSONException;
        void display(JSONObject content) throws Exception;
    }

    private Result delivery() throws Exception {
        Context context = getApplicationContext();
        return runDelivery(context, getInputData().getString("id"), this::isStopped, new DeliveryServices() {
            @Override public JSONObject fetch(String id, String credential) throws java.io.IOException, org.json.JSONException {
                return new PushApi().request("GET", "/api/v1/push/installation/deliveries/" + id, credential, null);
            }
            @Override public void display(JSONObject content) throws Exception { PushNotifications.display(context, content); }
        });
    }

    static Result runDelivery(Context context, String id, BooleanSupplier stopped, DeliveryServices services) throws Exception {
        PushStore store = new PushStore(context);
        if (!PushContract.uuid(id)) return Result.failure();
        JSONObject envelope; String credential; long revocation;
        synchronized (PushStore.LOCK) {
            JSONObject state = store.read(); JSONObject pending = state.optJSONObject("pending");
            envelope = pending == null ? null : pending.optJSONObject(id);
            if (envelope == null) return Result.success();
            if (PushWork.locallyBlocked || !new AuthStore(context).ownsPush(state)
                    || !PushStore.matches(state, envelope, System.currentTimeMillis()) || !PushNotifications.permitted(context)) {
                boolean rebindHold = PushSdk.bindingInProgress || (!state.optBoolean("optedOut")
                        && state.optJSONObject("installation") != null && !state.optString("credential").isEmpty()
                        && new AuthStore(context).ownsPush(state));
                if (rebindHold && PushWork.locallyBlocked) {
                    store.deliveryStage(PushStore.DeliveryStage.LOCALLY_BLOCKED);
                    return Result.success();
                }
                PushStore.DeliveryStage stage = PushWork.locallyBlocked ? PushStore.DeliveryStage.LOCALLY_BLOCKED
                        : !PushNotifications.permitted(context) ? PushStore.DeliveryStage.PERMISSION_DENIED : PushStore.DeliveryStage.BINDING_MISMATCH;
                if (stage == PushStore.DeliveryStage.BINDING_MISMATCH) try {
                    if (PushContract.instant(envelope.optString("expiresAt")) <= System.currentTimeMillis()) stage = PushStore.DeliveryStage.EXPIRED;
                } catch (RuntimeException ignored) { /* Diagnostic classification must not change rejection behavior. */ }
                store.deliveryStage(stage);
                store.drop(id); PushWork.permissionsChanged(context); return Result.success();
            }
            if (stopped.getAsBoolean()) return Result.success();
            int attempts = envelope.optInt("fetchAttempts");
            if (attempts >= 3) { store.drop(id); return Result.success(); }
            credential = state.getString("credential"); revocation = PushWork.localRevocation;
            store.update(current -> current.getJSONObject("pending").getJSONObject(id).put("fetchAttempts", attempts + 1));
            store.deliveryStage(PushStore.DeliveryStage.FETCH_STARTED);
        }
        try {
            JSONObject content = PushContract.delivery(services.fetch(id, credential), id, System.currentTimeMillis());
            synchronized (PushStore.LOCK) {
                if (!currentDelivery(store.read(), envelope, credential, revocation)) return Result.success();
                store.deliveryStage(PushStore.DeliveryStage.FETCHED);
                // A constraint stop is resumable, including a stop while waiting for this lock.
                if (stopped.getAsBoolean()) return Result.success();
                if (!PushNotifications.permitted(context)) { store.drop(id); return Result.success(); }
                store.commitDelivery(id, content, () -> {
                    try { services.display(content); }
                    catch (Exception ignored) { store.deliveryStage(PushStore.DeliveryStage.DISPLAY_FAILED); }
                });
            }
            return Result.success();
        } catch (PushApi.Failure failure) {
            synchronized (PushStore.LOCK) {
                if (!currentDelivery(store.read(), envelope, credential, revocation)) return Result.success();
                store.deliveryStage(failure.status == 401 || failure.status == 403 ? PushStore.DeliveryStage.FETCH_UNAUTHORIZED
                        : failure.status == 404 ? PushStore.DeliveryStage.FETCH_NOT_FOUND
                        : failure.status >= 500 ? PushStore.DeliveryStage.FETCH_TRANSIENT : PushStore.DeliveryStage.FETCH_FAILED);
                if (failure.status == 401) PushWork.unauthorized(context, credential);
                if (failure.status >= 500) return deliveryRetry(store, id, stopped);
                store.drop(id); return Result.success();
            }
        } catch (java.io.IOException failure) {
            synchronized (PushStore.LOCK) {
                if (!currentDelivery(store.read(), envelope, credential, revocation)) return Result.success();
                store.deliveryStage(PushStore.DeliveryStage.FETCH_TRANSIENT);
                return deliveryRetry(store, id, stopped);
            }
        } catch (RuntimeException | org.json.JSONException invalid) {
            synchronized (PushStore.LOCK) {
                if (!currentDelivery(store.read(), envelope, credential, revocation)) return Result.success();
                store.deliveryStage(PushStore.DeliveryStage.FETCH_FAILED);
                store.drop(id); return Result.success();
            }
        }
    }

    private static boolean currentDelivery(JSONObject state, JSONObject envelope, String credential, long revocation) {
        JSONObject pending = state.optJSONObject("pending");
        JSONObject current = pending == null ? null : pending.optJSONObject(envelope.optString("deliveryId"));
        JSONObject installation = state.optJSONObject("installation");
        return !PushWork.locallyBlocked && revocation == PushWork.localRevocation && credential.equals(state.optString("credential"))
                && current != null && installation != null
                && envelope.optString("installationId").equals(current.optString("installationId"))
                && envelope.optString("bindingGeneration").equals(current.optString("bindingGeneration"))
                && envelope.optString("installationId").equals(installation.optString("id"))
                && envelope.optString("bindingGeneration").equals(Long.toString(installation.optLong("bindingGeneration")));
    }

    // Called under LOCK: admitted fetch attempts consume the durable three-attempt budget.
    private static Result deliveryRetry(PushStore store, String id, BooleanSupplier stopped) throws Exception {
        if (stopped.getAsBoolean()) return Result.success();
        JSONObject pending = store.read().optJSONObject("pending");
        JSONObject envelope = pending == null ? null : pending.optJSONObject(id);
        if (envelope == null) return Result.success();
        if (envelope.optInt("fetchAttempts") < 3) return Result.retry();
        store.drop(id); return Result.success();
    }

    private Result registration() throws Exception {
        Context context = getApplicationContext(); PushStore store = new PushStore(context);
        JSONObject state = store.read(); String credential = state.optString("credential");
        if (PushWork.locallyBlocked || !new AuthStore(context).ownsPush(state)
                || !state.optBoolean("enabled") || credential.isEmpty() || isStopped()) return Result.success();
        PushApi api = new PushApi();
        try {
            if (PushNotifications.permitted(context))
                awaitToken(30000, () -> !isStopped(), result -> PushSdk.token(context, result));
            JSONObject install = PushContract.installation(api.request("GET", "/api/v1/push/installation", credential, null));
            PushWork.accept(context, install, credential);
            if ("session_expired".equals(install.getString("status")) || "session_revoked".equals(install.getString("status")))
                return Result.success();
            state = store.read();
            if (!state.optBoolean("enabled") || !credential.equals(state.optString("credential")) || isStopped()) return Result.success();
            String token = state.optString("token"); String provider = PushSdk.provider(state); String permission = PushNotifications.permitted(context) ? "granted" : "denied";
            if (token.equals(state.optString("registeredToken")) && provider.equals(state.optString("registeredProvider", "rustore")) && permission.equals(state.optString("registeredPermission"))) return Result.success();
            if (!PushContract.provider(provider) || !provider.equals(install.optString("provider")) && !PushContract.token(token)) return retry();
            JSONObject body = new JSONObject().put("registrationVersion", install.getLong("registrationVersion"))
                    .put("provider", provider).put("permission", permission);
            if (PushContract.token(token)) body.put("token", token);
            JSONObject result = api.request("PUT", "/api/v1/push/installation", credential, body);
            if (isStopped()) return Result.success();
            PushWork.accept(context, result, credential);
            store.update(current -> {
                if (credential.equals(current.optString("credential"))) current.put("registeredToken", token).put("registeredProvider", provider).put("registeredPermission", permission);
            });
            return Result.success();
        } catch (PushApi.Failure failure) {
            if (failure.status == 401 || failure.status == 404) { PushWork.unauthorized(context, credential); return Result.success(); }
            if (failure.status == 409) {
                PushWork.accept(context, api.request("GET", "/api/v1/push/installation", credential, null), credential);
                return retry();
            }
            return failure.status >= 500 ? retry() : Result.failure();
        } catch (java.io.IOException failure) { return retry(); }
    }

    private Result revoke() throws Exception {
        Context context = getApplicationContext();
        if (PushWork.revokePending(context, false)) return Result.success();
        JSONArray retired = new PushStore(context).read().optJSONArray("retired");
        if (retired != null) for (int i = 0; i < retired.length(); i++)
            if (retired.getJSONObject(i).optInt("attempts") < 3) return Result.retry();
        return Result.success();
    }
}
