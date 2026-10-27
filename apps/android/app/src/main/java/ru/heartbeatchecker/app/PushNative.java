package ru.heartbeatchecker.app;

import android.app.Application;
import android.content.Context;
import android.os.Build;
import android.util.Base64;

import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.util.UUID;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;

/** Native bearer binding while opaque delivery remains entirely in WorkManager. */
final class PushNative {
    static final class TransferRequired extends IOException { TransferRequired() {super("push_account_change_required");} }
    private static final String ORIGIN = "https://heartbeat-checker.ru";
    private static final int RESPONSE_LIMIT = 16 * 1024;

    private PushNative() {}

    static void toggle(Context context, boolean enabled, boolean allowAccountChange) throws Exception {
        if (BuildConfig.ISOLATED_API || BuildConfig.DEV_CLIENT) throw new IOException("Push unavailable in this app");
        if (!enabled) {PushWork.disable(context); return;}
        AuthStore auth = new AuthStore(context); JSONObject current = auth.active();
        if (current == null || !PushNotifications.permitted(context)) throw new IOException("Push permission or session unavailable");
        long epoch = auth.epoch();
        String accountId = current.getJSONObject("account").getString("id");
        JSONObject session = current.getJSONObject("session");
        String sessionId = session.getString("id"), access = session.getString("token");

        PushSdk.bindingInProgress = true;
        PushWork.blockLocally(context);
        long revocation = PushWork.localRevocation;
        PushStore store = new PushStore(context);
        try {
            store.update(state -> state.put("enabled", false));
            PushSdk.initialize((Application) context.getApplicationContext());
            JSONObject state = store.read();
            String credential = state.optString("credential");
            JSONObject pending = state.optJSONObject("pendingBind"), body;
            if (pending != null) {
                if (!accountId.equals(pending.optString("accountId")) || !sessionId.equals(pending.optString("sessionId"))
                        || credential.isEmpty() || pending.optJSONObject("body") == null
                        || !credential.equals(pending.getJSONObject("body").optString("installationCredential"))
                        || allowAccountChange != pending.getJSONObject("body").optBoolean("allowAccountChange"))
                    throw new IOException("Unresolved push binding belongs to another operation");
                body = pending.getJSONObject("body");
            } else {
                CountDownLatch completed = new CountDownLatch(1);
                AtomicReference<String> found = new AtomicReference<>();
                PushSdk.token(context, new PushSdk.TokenResult() {
                    @Override public void ready(String token) {found.set(token); completed.countDown();}
                    @Override public void unavailable() {completed.countDown();}
                });
                if (!completed.await(30, TimeUnit.SECONDS) || !PushContract.token(found.get())) throw new IOException("Push transport unavailable");
                if (auth.epoch() != epoch || auth.active() == null) throw new IOException("Session changed");
                synchronized (PushStore.LOCK) {
                    JSONObject latest = store.read();
                    // SDK acquisition can select a fresh route or race token rotation. Bind one current snapshot.
                    if (!sameBinding(state, latest) || latest.has("pendingBind") || PushWork.localRevocation != revocation
                            || auth.epoch() != epoch || auth.active() == null)
                        throw new IOException("Binding changed");
                    JSONObject snapshot = tokenSnapshot(latest);
                    credential = latest.optString("credential");
                    if (credential.isEmpty()) {
                        byte[] bytes = new byte[32]; new SecureRandom().nextBytes(bytes);
                        credential = UUID.randomUUID() + "." + Base64.encodeToString(bytes, Base64.URL_SAFE | Base64.NO_WRAP | Base64.NO_PADDING);
                    }
                    JSONObject installation = latest.optJSONObject("installation");
                    body = new JSONObject().put("installationCredential", credential).put("provider", snapshot.getString("provider"))
                            .put("token", snapshot.getString("token")).put("permission", "granted")
                            .put("deviceLabel", (Build.MANUFACTURER + " " + Build.MODEL).trim())
                            .put("appVersion", BuildConfig.VERSION_NAME);
                    if (installation != null) body.put("bindingGeneration", installation.getLong("bindingGeneration"))
                            .put("registrationVersion", installation.getLong("registrationVersion"));
                    if (allowAccountChange) body.put("allowAccountChange", true);
                    String savedCredential = credential; JSONObject savedBody = body;
                    store.update(value -> value.put("credential", savedCredential).put("pendingBind",
                            new JSONObject().put("accountId", accountId).put("sessionId", sessionId).put("body", savedBody)
                                    .put("tokenRevision", snapshot.getLong("tokenRevision"))));
                }
            }
            if (auth.epoch() != epoch || auth.active() == null) throw new IOException("Session changed");
            JSONObject expectedBinding;
            synchronized (PushStore.LOCK) {
                expectedBinding = store.read();
                if (PushWork.localRevocation != revocation || !credential.equals(expectedBinding.optString("credential"))
                        || !expectedBinding.has("pendingBind")) throw new IOException("Binding changed");
            }
            JSONObject result;
            try { result = request("POST", "/api/v1/push/native/bind", access, null, body); }
            catch (Failure failure) {
                if (failure.status == 409 && "push_account_change_required".equals(failure.code)) {
                    store.update(value -> value.remove("pendingBind"));
                    throw new TransferRequired();
                }
                if (failure.status != 409) throw failure;
                // A lost bind response can turn an exact retry into a CAS conflict.
                // Read the authoritative installation before deciding whether another POST is needed.
                try {result = request("GET", "/api/v1/push/native/installation", access, credential, null);}
                catch (Exception unavailable) {throw failure;}
            }
            if (auth.epoch() != epoch || auth.active() == null || !PushNotifications.permitted(context)) throw new IOException("Session changed");
            JSONObject bound = PushContract.installation(result.getJSONObject("installation"));
            synchronized (PushStore.LOCK) {
                JSONObject latest = store.read(), operation = latest.optJSONObject("pendingBind");
                if (auth.epoch() != epoch || auth.active() == null || PushWork.localRevocation != revocation || !PushNotifications.permitted(context)
                        || !accountId.equals(result.getString("accountId")) || !sessionId.equals(result.getString("sessionId"))
                        || !credential.equals(latest.optString("credential")) || operation == null
                        || !sameBinding(expectedBinding, latest)
                        || !accountId.equals(operation.optString("accountId")) || !sessionId.equals(operation.optString("sessionId"))
                        || !body.toString().equals(operation.getJSONObject("body").toString())
                        || !bindingTokenMatches(latest, body, operation.optLong("tokenRevision", -1))) throw new IOException("Push owner mismatch");
                // A 409 lookup may still expose the server's old route. Keep our current
                // provider/token; resumed registration reconciles them with the authoritative version.
                store.acceptInstallation(bound, credential, accountId, sessionId);
                PushWork.locallyBlocked = false;
            }
            PushWork.resume(context);
        } finally {PushSdk.bindingInProgress = false;}
    }

    static JSONObject tokenSnapshot(JSONObject state) throws IOException {
        String provider = PushSdk.provider(state), token = state.optString("token");
        if (!PushContract.provider(provider) || !PushContract.token(token)) throw new IOException("Push provider unavailable");
        try { return new JSONObject().put("provider", provider).put("token", token).put("tokenRevision", state.optLong("tokenRevision")); }
        catch (org.json.JSONException invalid) { throw new IOException("Push state unavailable"); }
    }

    static boolean sameBinding(JSONObject before, JSONObject after) {
        if (!before.optString("credential").equals(after.optString("credential"))) return false;
        JSONObject previous = before.optJSONObject("installation"), current = after.optJSONObject("installation");
        return previous == null ? current == null : current != null && previous.optString("id").equals(current.optString("id"))
                && previous.optLong("bindingGeneration") == current.optLong("bindingGeneration")
                && previous.optLong("registrationVersion") == current.optLong("registrationVersion");
    }

    static boolean bindingTokenMatches(JSONObject state, JSONObject body, long revision) {
        if (!body.optString("provider").equals(PushSdk.provider(state))) return false;
        long currentRevision = state.optLong("tokenRevision");
        return revision < 0 || currentRevision >= revision && PushContract.token(state.optString("token"))
                && (currentRevision > revision || body.optString("token").equals(state.optString("token")));
    }

    static final class Failure extends IOException {
        final int status; final String code; final int retryAfterSeconds;
        Failure(int status, String code) {this(status, code, 0);}
        Failure(int status, String code, int retryAfterSeconds) {super("Push request failed"); this.status = status; this.code = code; this.retryAfterSeconds = retryAfterSeconds;}
    }

    static JSONObject test(Context context, String requestId, String id) throws Exception {
        if (BuildConfig.ISOLATED_API || BuildConfig.DEV_CLIENT || (requestId == null) == (id == null) || requestId != null && !PushContract.uuid(requestId)
                || id != null && !PushContract.uuid(id)) throw new IOException("Test unavailable");
        AuthStore auth = new AuthStore(context); PushStore store = new PushStore(context);
        JSONObject active = auth.active(); long epoch = auth.epoch(); JSONObject before = store.read();
        JSONObject installation = before.optJSONObject("installation");
        if (active == null || PushWork.locallyBlocked || !auth.ownsPush(before) || installation == null
                || !PushContract.credential(before.optString("credential"), installation.optString("id"))
                || requestId != null && (!PushStore.ready(before, System.currentTimeMillis()) || !PushNotifications.permitted(context)))
            throw new IOException("Push not ready");
        String account = active.getJSONObject("account").getString("id"), session = active.getJSONObject("session").getString("id");
        String credential = before.getString("credential"); JSONObject install = before.getJSONObject("installation");
        if (requestId != null) reserveTest(context, requestId, epoch, account, session, credential, install);
        String path = "/api/v1/push/native/tests" + (id == null ? "" : "/" + id);
        JSONObject result;
        try {result = PushContract.nativeTest(request(id == null ? "POST" : "GET", path,
                active.getJSONObject("session").getString("token"), credential,
                id == null ? new JSONObject().put("requestId", requestId) : null));}
        catch (Failure failure) {
            if (requestId != null && failure.status >= 400 && failure.status < 500 && failure.status != 408)
                synchronized (PushStore.LOCK) {
                    JSONObject state = store.read();
                    if (testOwner(auth, state, epoch, account, session, credential, install))
                        store.update(value -> {JSONObject pending = value.optJSONObject("pendingTest");
                            if (testScope(pending, account, session, install) && requestId.equals(pending.optString("requestId"))) value.remove("pendingTest");});
                }
            throw failure;
        }
        synchronized (PushStore.LOCK) {
            JSONObject state = store.read();
            if (!testOwner(auth, state, epoch, account, session, credential, install)) throw new IOException("Binding changed");
            if (id != null && !id.equals(result.getJSONObject("test").getString("id"))) throw new IOException("Test mismatch");
            store.update(value -> {JSONObject pending = value.optJSONObject("pendingTest");
                JSONObject previousTest = pending == null ? null : pending.optJSONObject("test");
                if (testScope(pending, account, session, install) && (requestId != null && requestId.equals(pending.optString("requestId"))
                        || id != null && previousTest != null && id.equals(previousTest.optString("id"))))
                    pending.put("test", result.getJSONObject("test"));});
        }
        return result;
    }

    static boolean testScope(JSONObject pending, String account, String session, JSONObject install) {
        return pending != null && account.equals(pending.optString("accountId")) && session.equals(pending.optString("sessionId"))
                && install.optString("id").equals(pending.optString("installationId"))
                && install.optLong("bindingGeneration") == pending.optLong("bindingGeneration");
    }

    private static boolean testOwner(AuthStore auth, JSONObject state, long epoch, String account, String session,
                                     String credential, JSONObject install) throws Exception {
        JSONObject active = auth.active(), current = state.optJSONObject("installation");
        return active != null && auth.epoch() == epoch && !PushWork.locallyBlocked && auth.ownsPush(state) && current != null
                && account.equals(active.getJSONObject("account").getString("id")) && session.equals(active.getJSONObject("session").getString("id"))
                && credential.equals(state.optString("credential")) && install.getString("id").equals(current.optString("id"))
                && install.getLong("bindingGeneration") == current.optLong("bindingGeneration");
    }

    static void reserveTest(Context context, String requestId, long epoch, String account, String session,
                            String credential, JSONObject install) throws Exception {
        synchronized (PushStore.LOCK) {
            PushStore store = new PushStore(context); AuthStore auth = new AuthStore(context);
            if (!testOwner(auth, store.read(), epoch, account, session, credential, install)) throw new IOException("Binding changed");
            store.update(state -> {
                JSONObject pending = state.optJSONObject("pendingTest");
                if (!testScope(pending, account, session, install)) pending = null;
                JSONObject previousTest = pending == null ? null : pending.optJSONObject("test");
                boolean unresolved = previousTest == null || PushContract.instant(previousTest.getString("nextAllowedAt")) > System.currentTimeMillis();
                if (pending != null && unresolved && System.currentTimeMillis() - pending.optLong("createdAt") < 10 * 60_000
                        && !requestId.equals(pending.optString("requestId"))) throw new org.json.JSONException("Resolve previous test first");
                if (pending == null || !requestId.equals(pending.optString("requestId")))
                    state.put("pendingTest", new JSONObject().put("requestId", requestId).put("createdAt", System.currentTimeMillis())
                            .put("accountId", account).put("sessionId", session).put("installationId", install.getString("id"))
                            .put("bindingGeneration", install.getLong("bindingGeneration")));
            });
        }
    }

    static JSONObject restoreTest(Context context) throws Exception {
        if (BuildConfig.ISOLATED_API || BuildConfig.DEV_CLIENT) return null;
        JSONObject state = new PushStore(context).read(), pending = state.optJSONObject("pendingTest"), install = state.optJSONObject("installation");
        if (pending == null || install == null || !new AuthStore(context).ownsPush(state) || PushWork.locallyBlocked
                || !pending.optString("accountId").equals(state.optString("accountId")) || !pending.optString("sessionId").equals(state.optString("sessionId"))
                || !pending.optString("installationId").equals(install.optString("id"))
                || pending.optLong("bindingGeneration") != install.optLong("bindingGeneration")
                || !PushContract.uuid(pending.optString("requestId")) || System.currentTimeMillis() - pending.optLong("createdAt") >= 10 * 60_000) return null;
        JSONObject result = new JSONObject().put("requestId", pending.getString("requestId"));
        if (pending.optJSONObject("test") != null) result.put("test", pending.getJSONObject("test"));
        return result;
    }

    private static JSONObject request(String method, String path, String bearer, String installationCredential, JSONObject body) throws Exception {
        if (!path.equals("/api/v1/push/native/bind") && !path.equals("/api/v1/push/native/installation")
                && !path.matches("/api/v1/push/native/tests(?:/[0-9a-f-]{36})?")) throw new IOException();
        HttpURLConnection connection = (HttpURLConnection) new URL(ORIGIN + path).openConnection();
        try {
            connection.setInstanceFollowRedirects(false); connection.setConnectTimeout(5000); connection.setReadTimeout(5000);
            connection.setRequestMethod(method); connection.setRequestProperty("Accept", "application/json");
            connection.setRequestProperty("Cache-Control", "no-store");
            connection.setRequestProperty("Authorization", "Bearer " + bearer);
            if (installationCredential != null) connection.setRequestProperty("X-Heartbeat-Installation-Credential", installationCredential);
            if (body != null) {
                byte[] data = body.toString().getBytes(StandardCharsets.UTF_8);
                connection.setDoOutput(true); connection.setRequestProperty("Content-Type", "application/json");
                connection.setFixedLengthStreamingMode(data.length);
                try (java.io.OutputStream output = connection.getOutputStream()) {output.write(data);}
            }
            int status = connection.getResponseCode();
            InputStream input = status < 400 ? connection.getInputStream() : connection.getErrorStream();
            if (input == null) throw new Failure(status, "unknown");
            try (InputStream stream = input; ByteArrayOutputStream output = new ByteArrayOutputStream()) {
                byte[] buffer = new byte[1024]; int count;
                while ((count = stream.read(buffer)) != -1) {
                    if (output.size() + count > RESPONSE_LIMIT) throw new IOException("Push response too large");
                    output.write(buffer, 0, count);
                }
                JSONObject value = new JSONObject(output.toString(StandardCharsets.UTF_8.name()));
                if (status < 200 || status >= 300) {
                    int retry = 0;
                    try {retry = Math.max(0, Math.min(3600, Integer.parseInt(connection.getHeaderField("Retry-After"))));}
                    catch (RuntimeException ignored) { }
                    throw new Failure(status, value.optString("error"), retry);
                }
                return value;
            }
        } finally {connection.disconnect();}
    }
}
