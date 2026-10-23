package ru.heartbeatchecker.app;

import android.content.Context;
import androidx.work.ListenableWorker.Result;
import org.json.JSONObject;
import java.util.Iterator;
import java.util.function.BooleanSupplier;

/** Test content and receipt capabilities never cross the React Native bridge. */
final class PushTests {
    private PushTests() {}

    interface Services {
        JSONObject fetch(String id, String credential) throws Exception;
        boolean display(JSONObject content) throws Exception;
    }

    static Result delivery(Context context, String id, BooleanSupplier stopped) throws Exception {
        return delivery(context, id, stopped, new Services() {
            public JSONObject fetch(String key, String credential) throws Exception {
                return new PushApi().request("GET", "/api/v1/push/installation/test-deliveries/" + key, credential, null);
            }
            public boolean display(JSONObject content) throws Exception {return PushNotifications.displayTest(context, content);}
        });
    }

    static boolean current(Context context, JSONObject state, JSONObject envelope, String credential, long revocation) {
        return !PushWork.locallyBlocked && revocation == PushWork.localRevocation
                && credential.equals(state.optString("credential")) && new AuthStore(context).ownsPush(state)
                && PushStore.matches(state, envelope, System.currentTimeMillis());
    }

    static Result delivery(Context context, String id, BooleanSupplier stopped, Services services) throws Exception {
        if (!PushContract.uuid(id)) return Result.failure();
        PushStore store = new PushStore(context); JSONObject envelope; String credential; long revocation;
        synchronized (PushStore.LOCK) {
            JSONObject state = store.read(), pending = state.optJSONObject("pending");
            envelope = pending == null ? null : pending.optJSONObject(id);
            if (envelope == null || !"2".equals(envelope.optString("v"))) return Result.success();
            credential = state.optString("credential"); revocation = PushWork.localRevocation;
            if (!current(context, state, envelope, credential, revocation)) {store.drop(id); return Result.success();}
            if (stopped.getAsBoolean()) return Result.success();
            int attempts = envelope.optInt("fetchAttempts");
            if (attempts >= 3) {store.drop(id); return Result.success();}
            store.update(value -> value.getJSONObject("pending").getJSONObject(id).put("fetchAttempts", attempts + 1));
        }
        try {
            JSONObject content = PushContract.testDelivery(services.fetch(id, credential), id, System.currentTimeMillis());
            if (PushContract.instant(content.getString("expiresAt")) != PushContract.instant(envelope.getString("expiresAt")))
                throw new org.json.JSONException("Test lifetime mismatch");
            synchronized (PushStore.LOCK) {
                if (!current(context, store.read(), envelope, credential, revocation)) return Result.success();
                store.testReceipt(envelope, content, "received");
                PushWork.receipts(context);
                if (stopped.getAsBoolean()) return Result.success();
                store.commitTestDelivery(id, () -> {
                    boolean submitted = false;
                    try {submitted = services.display(content);}
                    catch (Exception ignored) { /* Never report a failed notify as submitted. */ }
                    try {store.testReceipt(envelope, content, submitted ? "display_submitted" : "display_failed");}
                    catch (Exception ignored) { /* Durable received receipt remains recoverable. */ }
                });
            }
            PushWork.receipts(context);
            return Result.success();
        } catch (PushApi.Failure failure) {
            synchronized (PushStore.LOCK) {
                if (!current(context, store.read(), envelope, credential, revocation)) return Result.success();
                if (failure.status == 401) PushWork.unauthorized(context, credential);
                if (failure.status >= 500) return retry(store, id, stopped);
                store.drop(id); return Result.success();
            }
        } catch (java.io.IOException failure) {
            synchronized (PushStore.LOCK) {
                if (!current(context, store.read(), envelope, credential, revocation)) return Result.success();
                return retry(store, id, stopped);
            }
        } catch (org.json.JSONException | RuntimeException invalid) {
            synchronized (PushStore.LOCK) {
                if (current(context, store.read(), envelope, credential, revocation)) store.drop(id);
            }
            return Result.success();
        }
    }

    private static Result retry(PushStore store, String id, BooleanSupplier stopped) throws Exception {
        if (stopped.getAsBoolean()) return Result.success();
        JSONObject pending = store.read().optJSONObject("pending"), envelope = pending == null ? null : pending.optJSONObject(id);
        if (envelope != null && envelope.optInt("fetchAttempts") < 3) return Result.retry();
        store.drop(id); return Result.success();
    }

    static Result receipts(Context context, BooleanSupplier stopped) throws Exception {
        PushStore store = new PushStore(context);
        JSONObject queue = store.read().optJSONObject("testReceipts");
        if (queue == null) return Result.success();
        boolean retry = false;
        for (Iterator<String> ids = queue.keys(); ids.hasNext();) {
            String id = ids.next();
            for (String stage : new String[]{"received", "display_submitted", "display_failed"}) {
                if (stopped.getAsBoolean()) return Result.success();
                String credential, token; JSONObject envelope; long revocation;
                synchronized (PushStore.LOCK) {
                    JSONObject state = store.read(), items = state.optJSONObject("testReceipts"), item = items == null ? null : items.optJSONObject(id);
                    if (item == null) break;
                    envelope = item.getJSONObject("envelope"); credential = state.optString("credential"); revocation = PushWork.localRevocation;
                    if (!current(context, state, envelope, credential, revocation)) {
                        store.update(value -> value.getJSONObject("testReceipts").remove(id)); break;
                    }
                    JSONObject stages = item.getJSONObject("stages");
                    if (!stages.has(stage) || stages.optInt(stage) >= 3) continue;
                    int attempts = stages.getInt(stage); token = item.getString("receiptToken");
                    store.update(value -> value.getJSONObject("testReceipts").getJSONObject(id).getJSONObject("stages").put(stage, attempts + 1));
                }
                boolean done = false;
                try {
                    // Deliberately outside PushStore.LOCK: logout/rebinding must not wait for the network.
                    new PushApi().request("POST", "/api/v1/push/installation/test-deliveries/" + id + "/receipt", credential,
                            new JSONObject().put("receiptToken", token).put("stage", stage));
                    done = true;
                } catch (PushApi.Failure failure) {
                    done = failure.status == 400 || failure.status == 401 || failure.status == 403 || failure.status == 404 || failure.status == 410;
                } catch (java.io.IOException ignored) { /* Bounded durable retry. */ }
                synchronized (PushStore.LOCK) {
                    JSONObject state = store.read(), items = state.optJSONObject("testReceipts"), item = items == null ? null : items.optJSONObject(id);
                    if (item == null || !current(context, state, envelope, credential, revocation)) continue;
                    if (done) store.update(value -> {
                        JSONObject records = value.getJSONObject("testReceipts"), record = records.optJSONObject(id);
                        if (record == null) return;
                        JSONObject stages = record.getJSONObject("stages"); stages.remove(stage);
                        if (stages.length() == 0) records.remove(id);
                    });
                    else if (item.getJSONObject("stages").optInt(stage) < 3) retry = true;
                }
            }
        }
        return retry ? Result.retry() : Result.success();
    }
}
