package ru.heartbeatchecker.app;

import android.content.Context;

import androidx.annotation.NonNull;
import androidx.work.Worker;
import androidx.work.WorkerParameters;

import org.json.JSONArray;
import org.json.JSONObject;

public final class PushWorker extends Worker {
    public PushWorker(@NonNull Context context, @NonNull WorkerParameters parameters) { super(context, parameters); }

    @NonNull @Override public Result doWork() {
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

    private Result delivery() throws Exception {
        Context context = getApplicationContext(); PushStore store = new PushStore(context);
        String id = getInputData().getString("id"); if (!PushContract.uuid(id)) return Result.failure();
        JSONObject state = store.read(); JSONObject pending = state.optJSONObject("pending");
        JSONObject envelope = pending == null ? null : pending.optJSONObject(id);
        if (envelope == null) return Result.success();
        if (PushWork.locallyBlocked || !PushStore.matches(state, envelope, System.currentTimeMillis()) || !PushNotifications.permitted(context)) {
            store.drop(id); PushWork.permissionsChanged(context); return Result.success();
        }
        int attempts = envelope.optInt("fetchAttempts");
        if (attempts >= 3 || isStopped()) { store.drop(id); return Result.success(); }
        store.update(current -> {
            JSONObject jobs = current.optJSONObject("pending");
            if (jobs != null && jobs.has(id)) jobs.getJSONObject(id).put("fetchAttempts", attempts + 1);
        });
        String credential = state.getString("credential");
        try {
            JSONObject result = new PushApi().request("GET", "/api/v1/push/installation/deliveries/" + id, credential, null);
            JSONObject content = PushContract.delivery(result, id, System.currentTimeMillis());
            if (isStopped()) return Result.success();
            synchronized (PushStore.LOCK) {
                if (PushWork.locallyBlocked || !PushNotifications.permitted(context)) { store.drop(id); return Result.success(); }
                store.commitDelivery(id, content, () -> {
                    try { PushNotifications.display(context, content); }
                    catch (Exception ignored) { /* Permission can change between check and notify. */ }
                });
            }
            return Result.success();
        } catch (PushApi.Failure failure) {
            if (failure.status == 401) PushWork.unauthorized(context, credential);
            if (failure.status >= 500 && attempts < 2 && !isStopped()) return Result.retry();
            store.drop(id); return Result.success();
        } catch (java.io.IOException failure) {
            if (attempts < 2 && !isStopped()) return Result.retry();
            store.drop(id); return Result.success();
        } catch (RuntimeException | org.json.JSONException invalid) {
            store.drop(id); return Result.success();
        }
    }

    private Result registration() throws Exception {
        Context context = getApplicationContext(); PushStore store = new PushStore(context);
        JSONObject state = store.read(); String credential = state.optString("credential");
        if (PushWork.locallyBlocked || !state.optBoolean("enabled") || credential.isEmpty() || isStopped()) return Result.success();
        PushApi api = new PushApi();
        try {
            JSONObject install = PushContract.installation(api.request("GET", "/api/v1/push/installation", credential, null));
            PushWork.accept(context, install, credential);
            if ("session_expired".equals(install.getString("status")) || "session_revoked".equals(install.getString("status")))
                return Result.success();
            state = store.read();
            if (!state.optBoolean("enabled") || !credential.equals(state.optString("credential")) || isStopped()) return Result.success();
            String token = state.optString("token"); String permission = PushNotifications.permitted(context) ? "granted" : "denied";
            if (token.equals(state.optString("registeredToken")) && permission.equals(state.optString("registeredPermission"))) return Result.success();
            JSONObject body = new JSONObject().put("registrationVersion", install.getLong("registrationVersion")).put("permission", permission);
            if (PushContract.token(token)) body.put("token", token);
            JSONObject result = api.request("PUT", "/api/v1/push/installation", credential, body);
            if (isStopped()) return Result.success();
            PushWork.accept(context, result, credential);
            store.update(current -> {
                if (credential.equals(current.optString("credential"))) current.put("registeredToken", token).put("registeredPermission", permission);
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
