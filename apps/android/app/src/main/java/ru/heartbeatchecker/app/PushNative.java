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
        if ("test".equals(BuildConfig.BILLING_ENVIRONMENT)) throw new IOException("Push disabled in isolated SDK test app");
        if (!enabled) {PushWork.disable(context); return;}
        AuthStore auth = new AuthStore(context); JSONObject current = auth.active();
        if (current == null || !PushNotifications.permitted(context)) throw new IOException("Push permission or session unavailable");
        long epoch = auth.epoch();
        String accountId = current.getJSONObject("account").getString("id");
        JSONObject session = current.getJSONObject("session");
        String sessionId = session.getString("id"), access = session.getString("token");

        PushSdk.bindingInProgress = true;
        PushWork.blockLocally(context);
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
                String provider = PushSdk.provider(state);
                if (!PushContract.provider(provider)) throw new IOException("Push provider unavailable");
                if (credential.isEmpty()) {
                    byte[] bytes = new byte[32]; new SecureRandom().nextBytes(bytes);
                    credential = UUID.randomUUID() + "." + Base64.encodeToString(bytes, Base64.URL_SAFE | Base64.NO_WRAP | Base64.NO_PADDING);
                }
                JSONObject installation = state.optJSONObject("installation");
                body = new JSONObject().put("installationCredential", credential).put("provider", provider)
                        .put("token", found.get()).put("permission", "granted")
                        .put("deviceLabel", (Build.MANUFACTURER + " " + Build.MODEL).trim())
                        .put("appVersion", "2.0.0");
                if (installation != null) body.put("bindingGeneration", installation.getLong("bindingGeneration"))
                        .put("registrationVersion", installation.getLong("registrationVersion"));
                if (allowAccountChange) body.put("allowAccountChange", true);
                String savedCredential = credential; JSONObject savedBody = body;
                store.update(value -> value.put("credential", savedCredential).put("pendingBind",
                        new JSONObject().put("accountId", accountId).put("sessionId", sessionId).put("body", savedBody)));
            }
            if (auth.epoch() != epoch || auth.active() == null) throw new IOException("Session changed");
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
            if (!accountId.equals(result.getString("accountId")) || !sessionId.equals(result.getString("sessionId"))
                    || !credential.equals(store.read().optString("credential"))) throw new IOException("Push owner mismatch");
            store.acceptInstallation(bound, credential, accountId, sessionId);
            PushWork.locallyBlocked = false;
            PushWork.resume(context);
        } finally {PushSdk.bindingInProgress = false;}
    }

    private static final class Failure extends IOException {
        final int status; final String code;
        Failure(int status, String code) {super("Push binding failed"); this.status = status; this.code = code;}
    }

    private static JSONObject request(String method, String path, String bearer, String installationCredential, JSONObject body) throws Exception {
        if (!path.equals("/api/v1/push/native/bind") && !path.equals("/api/v1/push/native/installation")) throw new IOException();
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
                if (status < 200 || status >= 300) throw new Failure(status, value.optString("error"));
                return value;
            }
        } finally {connection.disconnect();}
    }
}
