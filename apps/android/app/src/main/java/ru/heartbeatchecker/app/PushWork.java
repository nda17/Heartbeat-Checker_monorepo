package ru.heartbeatchecker.app;

import android.content.Context;
import android.os.Build;

import androidx.work.Constraints;
import androidx.work.Data;
import androidx.work.ExistingWorkPolicy;
import androidx.work.NetworkType;
import androidx.work.OneTimeWorkRequest;
import androidx.work.OutOfQuotaPolicy;
import androidx.work.WorkManager;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.IOException;
import java.util.Iterator;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

final class PushWork {
    static final String DELIVERY = "heartbeat-push-delivery";
    static final String REGISTRATION = "heartbeat-push-registration";
    static final ExecutorService IO = Executors.newSingleThreadExecutor();
    static volatile boolean locallyBlocked;
    static volatile long localRevocation;
    private PushWork() {}

    private static OneTimeWorkRequest work(String kind, String id, String tag) {
        return work(kind, id, tag, Build.VERSION.SDK_INT);
    }

    static OneTimeWorkRequest work(String kind, String id, String tag, int sdk) {
        OneTimeWorkRequest.Builder request = new OneTimeWorkRequest.Builder(PushWorker.class)
                .setConstraints(new Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
                .setInputData(new Data.Builder().putString("kind", kind).putString("id", id).build()).addTag(tag);
        if ("delivery".equals(kind) && sdk >= 31)
            request.setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST);
        return request.build();
    }

    static void delivery(Context context, JSONObject envelope) throws IOException, JSONException {
        PushStore store = new PushStore(context);
        boolean owner = new AuthStore(context).ownsPush(store.read());
        if (locallyBlocked || !owner || !PushNotifications.permitted(context)) {
            store.deliveryStage(locallyBlocked ? PushStore.DeliveryStage.LOCALLY_BLOCKED : PushStore.DeliveryStage.PERMISSION_DENIED);
            permissionsChanged(context); return;
        }
        if (store.reserve(envelope)) {
            store.deliveryStage(PushStore.DeliveryStage.QUEUED);
            enqueueDelivery(context, envelope.getString("deliveryId"));
        } else try {
            if (!PushStore.matches(store.read(), envelope, System.currentTimeMillis()))
                store.deliveryStage(PushContract.instant(envelope.optString("expiresAt")) <= System.currentTimeMillis()
                        ? PushStore.DeliveryStage.EXPIRED : PushStore.DeliveryStage.BINDING_MISMATCH);
        } catch (Exception ignored) {
            // Best-effort diagnostics never turn an existing rejection into an operation failure.
        }
    }

    private static void enqueueDelivery(Context context, String id) {
        WorkManager.getInstance(context).enqueueUniqueWork(DELIVERY + ":" + id, ExistingWorkPolicy.KEEP, work("delivery", id, DELIVERY));
    }

    static void registration(Context context) {
        WorkManager.getInstance(context).enqueueUniqueWork(REGISTRATION, ExistingWorkPolicy.REPLACE, work("registration", "", REGISTRATION));
    }

    static void permissionsChanged(Context context) {
        if (!PushNotifications.permitted(context)) {
            synchronized (PushStore.LOCK) {
                PushNotifications.clear(context);
                WorkManager.getInstance(context).cancelAllWorkByTag(DELIVERY);
            }
        }
        registration(context);
    }

    static void resume(Context context) {
        Context app = context.getApplicationContext();
        IO.execute(() -> {
            try {
                JSONObject state = new PushStore(app).read();
                if (state.has("retired")) revoke(app);
                if (state.optBoolean("enabled") && new AuthStore(app).ownsPush(state)) {
                    permissionsChanged(app);
                    JSONObject pending = state.optJSONObject("pending");
                    if (pending != null) for (Iterator<String> keys = pending.keys(); keys.hasNext();) enqueueDelivery(app, keys.next());
                }
            } catch (Exception ignored) { /* Corrupt/locked storage is never replaced automatically. */ }
        });
    }

    static void accept(Context context, JSONObject value, String credential) throws IOException, JSONException {
        JSONObject installation = PushContract.installation(value);
        if (!PushContract.credential(credential, installation.getString("id"))) throw new IOException("Installation mismatch");
        PushStore store = new PushStore(context);
        synchronized (PushStore.LOCK) {
            JSONObject state = store.read();
            if (!credential.equals(state.optString("credential"))) return;
            JSONObject before = state.optJSONObject("installation");
            store.acceptInstallation(installation, credential);
            if (before == null || before.optLong("bindingGeneration") != installation.getLong("bindingGeneration")
                    || !"ready".equals(installation.getString("status"))) {
                PushNotifications.clear(context);
                WorkManager.getInstance(context).cancelAllWorkByTag(DELIVERY);
            }
        }
    }

    static void unauthorized(Context context, String credential) throws IOException {
        synchronized (PushStore.LOCK) {
            PushStore store = new PushStore(context);
            if (!credential.equals(store.read().optString("credential"))) return;
            store.update(state -> {
                JSONObject installation = state.optJSONObject("installation");
                if (installation != null) installation.put("status", "session_revoked");
                state.remove("pending");
            });
            PushNotifications.clear(context);
        }
    }

    static void blockLocally(Context context) {
        synchronized (PushStore.LOCK) {
            localRevocation++;
            locallyBlocked = true;
        }
        try { PushNotifications.clear(context); } catch (RuntimeException ignored) { }
        try {
            WorkManager.getInstance(context).cancelAllWorkByTag(DELIVERY);
            WorkManager.getInstance(context).cancelAllWorkByTag(REGISTRATION);
        } catch (RuntimeException ignored) { }
    }

    static void disable(Context context) throws IOException {
        blockLocally(context);
        synchronized (PushStore.LOCK) {
            try { new PushStore(context).update(state -> {
                String credential = state.optString("credential");
                JSONArray retired = state.optJSONArray("retired"); if (retired == null) retired = new JSONArray();
                if (!credential.isEmpty()) {
                    retired.put(new JSONObject().put("credential", credential).put("attempts", 0));
                }
                state.put("retired", retired).put("enabled", false).put("optedOut", true);
                for (String key : new String[]{"credential", "installation", "pair", "token", "pending", "seen", "sequences", "pendingBind",
                        "registeredToken", "registeredPermission", "everExchangeAttempted"}) state.remove(key);
            }); } finally {
                blockLocally(context);
                // Server revocation owns account access. Deleting the SDK token asynchronously
                // could invalidate a token reused by an immediate reconnect.
            }
        }
        revoke(context);
    }

    static void logout(Context context) throws IOException {
        blockLocally(context);
        synchronized (PushStore.LOCK) {
            new PushStore(context).update(state -> {
                String credential = state.optString("credential");
                JSONArray retired = state.optJSONArray("retired"); if (retired == null) retired = new JSONArray();
                if (!credential.isEmpty()) retired.put(new JSONObject().put("credential", credential).put("attempts", 0));
                state.put("retired", retired).put("enabled", false);
                for (String key : new String[]{"credential", "installation", "pair", "pending", "seen", "sequences", "pendingBind",
                        "registeredToken", "registeredPermission", "accountId", "sessionId"}) state.remove(key);
            });
        }
        revoke(context);
    }

    static boolean revokePending(Context context, boolean explicitRetry) throws IOException, JSONException {
        PushStore store = new PushStore(context);
        JSONArray retired = store.read().optJSONArray("retired");
        if (retired == null) return true;
        for (int i = 0; i < retired.length(); i++) {
            JSONObject item = retired.getJSONObject(i); String credential = item.getString("credential");
            int attempts = item.optInt("attempts");
            if (!explicitRetry && attempts >= 3) continue;
            boolean done = false;
            try { new PushApi().request("DELETE", "/api/v1/push/installation", credential, null); done = true; }
            catch (PushApi.Failure error) { done = error.status == 401 || error.status == 404; }
            catch (IOException ignored) { /* Keep unresolved revocation for an explicit retry. */ }
            boolean terminal = done;
            store.update(state -> {
                JSONArray queue = state.optJSONArray("retired"); if (queue == null) return;
                for (int index = queue.length() - 1; index >= 0; index--) {
                    JSONObject current = queue.getJSONObject(index);
                    if (!credential.equals(current.optString("credential"))) continue;
                    if (terminal) queue.remove(index);
                    else current.put("attempts", Math.min(3, Math.max(attempts, current.optInt("attempts")) + 1));
                }
                if (queue.length() == 0) state.remove("retired");
            });
        }
        JSONArray remaining = store.read().optJSONArray("retired");
        return remaining == null || remaining.length() == 0;
    }

    private static void revoke(Context context) {
        WorkManager.getInstance(context).enqueueUniqueWork("heartbeat-push-revoke", ExistingWorkPolicy.APPEND_OR_REPLACE,
                work("revoke", "", "heartbeat-push-revoke"));
    }
}
