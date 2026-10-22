package ru.heartbeatchecker.app;

import android.content.Context;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.AtomicFile;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.File;
import java.io.FileNotFoundException;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.util.Arrays;
import java.util.Iterator;

import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

final class PushStore {
    static final Object LOCK = new Object();
    private static final String ALIAS = "heartbeat.push.v1";
    private static final int LIMIT = 1024 * 1024;
    private final AtomicFile file;
    interface Change { void apply(JSONObject state) throws JSONException; }
    enum ProviderIssue { DISTRIBUTOR_MISSING, DISTRIBUTOR_AUTH_REQUIRED, DISTRIBUTOR_BACKGROUND_RESTRICTED, SDK_UNAVAILABLE, OTHER }
    enum DeliveryStage { RECEIVED, INVALID_ENVELOPE, LOCALLY_BLOCKED, PERMISSION_DENIED, BINDING_MISMATCH, EXPIRED,
        QUEUED, QUEUE_FAILED, FETCH_STARTED, FETCHED, DISPLAY_SUBMITTED, FETCH_UNAUTHORIZED, FETCH_NOT_FOUND, FETCH_TRANSIENT,
        FETCH_FAILED, DISPLAY_FAILED }

    PushStore(Context context) { file = new AtomicFile(new File(context.getNoBackupFilesDir(), "push-state.enc")); }

    boolean nearLimit() { return file.getBaseFile().length() > LIMIT - 8192; }

    void providerIssue(ProviderIssue issue) {
        if (issue == null) return;
        try { update(state -> {
            JSONObject previous = state.optJSONObject("providerDiagnostic");
            if (previous != null && (issue == ProviderIssue.SDK_UNAVAILABLE || issue == ProviderIssue.OTHER)) return;
            state.put("providerDiagnostic", new JSONObject().put("code", issue.name()).put("at", System.currentTimeMillis()));
        }); } catch (IOException ignored) { /* Diagnostics must never affect the push operation. */ }
    }

    void deliveryStage(DeliveryStage stage) { deliveryStage(stage, null); }

    void deliveryStage(DeliveryStage stage, PushContract.EnvelopeReason reason) {
        if (stage == null) return;
        try { update(state -> {
            long now = System.currentTimeMillis();
            if (stage == DeliveryStage.RECEIVED) state.put("lastReceivedAt", now);
            JSONObject diagnostic = new JSONObject().put("code", stage.name()).put("at", now);
            if (stage == DeliveryStage.INVALID_ENVELOPE && reason != null)
                diagnostic.put("reason", reason.name());
            state.put("deliveryDiagnostic", diagnostic);
        }); } catch (IOException ignored) { /* Keep a single bounded snapshot, without payloads or identifiers. */ }
    }

    JSONObject read() throws IOException {
        synchronized (LOCK) {
            try {
                byte[] bytes;
                try (java.io.InputStream input = file.openRead(); java.io.ByteArrayOutputStream output = new java.io.ByteArrayOutputStream()) {
                    byte[] buffer = new byte[4096]; int count;
                    while ((count = input.read(buffer)) != -1) {
                        if (output.size() + count > LIMIT + 64) throw new IOException("Push storage limit");
                        output.write(buffer, 0, count);
                    }
                    bytes = output.toByteArray();
                }
                if (bytes.length < 30 || bytes[0] != 1) throw new IOException("Invalid push storage");
                Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
                cipher.init(Cipher.DECRYPT_MODE, key(), new GCMParameterSpec(128, Arrays.copyOfRange(bytes, 1, 13)));
                return new JSONObject(new String(cipher.doFinal(Arrays.copyOfRange(bytes, 13, bytes.length)), StandardCharsets.UTF_8));
            } catch (FileNotFoundException absent) {
                if (file.getBaseFile().exists()) throw new IOException("Push storage unavailable");
                return new JSONObject();
            }
            catch (Exception error) { throw new IOException("Push storage unavailable"); }
        }
    }

    void update(Change change) throws IOException {
        synchronized (LOCK) {
            JSONObject state = read();
            try { change.apply(state); write(state); }
            catch (JSONException error) { throw new IOException("Invalid push state"); }
        }
    }

    private SecretKey key() throws Exception {
        KeyStore store = KeyStore.getInstance("AndroidKeyStore"); store.load(null);
        if (store.containsAlias(ALIAS)) return (SecretKey) store.getKey(ALIAS, null);
        KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
        generator.init(new KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true).build());
        return generator.generateKey();
    }

    private void write(JSONObject state) throws IOException {
        FileOutputStream output = null;
        try {
            byte[] body = state.toString().getBytes(StandardCharsets.UTF_8);
            if (body.length > LIMIT) throw new IOException("Push storage limit");
            Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding"); cipher.init(Cipher.ENCRYPT_MODE, key());
            byte[] encrypted = cipher.doFinal(body);
            output = file.startWrite(); output.write(1); output.write(cipher.getIV()); output.write(encrypted);
            file.finishWrite(output);
        } catch (Exception error) {
            if (output != null) file.failWrite(output);
            throw new IOException("Push storage unavailable");
        }
    }

    static boolean ready(JSONObject state, long now) {
        try {
            JSONObject install = state.getJSONObject("installation");
            return state.optBoolean("enabled") && "ready".equals(install.getString("status"))
                    && !install.isNull("leaseExpiresAt") && PushContract.instant(install.getString("leaseExpiresAt")) > now
                    && PushContract.credential(state.getString("credential"), install.getString("id"));
        } catch (Exception ignored) { return false; }
    }

    static boolean matches(JSONObject state, JSONObject envelope, long now) {
        try {
            JSONObject install = state.getJSONObject("installation");
            return ready(state, now) && install.getString("id").equals(envelope.getString("installationId"))
                    && install.getLong("bindingGeneration") == Long.parseLong(envelope.getString("bindingGeneration"))
                    && PushContract.instant(envelope.getString("expiresAt")) > now;
        } catch (Exception ignored) { return false; }
    }

    void acceptInstallation(JSONObject installation, String expectedCredential) throws IOException {
        acceptInstallation(installation, expectedCredential, null, null);
    }

    void acceptInstallation(JSONObject installation, String expectedCredential, String accountId, String sessionId) throws IOException {
        update(state -> {
            if (!expectedCredential.equals(state.optString("credential"))) throw new JSONException("Installation credential changed");
            JSONObject previous = state.optJSONObject("installation");
            if (previous != null && (installation.getLong("bindingGeneration") < previous.optLong("bindingGeneration")
                    || installation.getLong("registrationVersion") < previous.optLong("registrationVersion")))
                throw new JSONException("Stale installation response");
            if (previous == null || previous.optLong("bindingGeneration") != installation.getLong("bindingGeneration")) {
                state.remove("pending"); state.remove("seen"); state.remove("sequences");
                state.remove("testSeen"); state.remove("testReceipts"); state.remove("pendingTest");
            }
            state.put("installation", installation);
            if (accountId != null && sessionId != null) {
                state.put("accountId", accountId).put("sessionId", sessionId)
                        .put("enabled", true).put("optedOut", false);
                state.remove("pair"); state.remove("pendingBind");
            }
        });
    }

    boolean reserve(JSONObject envelope) throws IOException {
        synchronized (LOCK) {
            JSONObject state = read();
            try {
                long now = System.currentTimeMillis();
                if (!matches(state, envelope, now)) return false;
                String id = envelope.getString("deliveryId");
                JSONObject pending = state.optJSONObject("pending"); if (pending == null) pending = new JSONObject();
                for (Iterator<String> it = pending.keys(); it.hasNext();) {
                    String key = it.next();
                    if (PushContract.instant(pending.getJSONObject(key).getString("expiresAt")) <= now) it.remove();
                }
                if (seen(state, id, "2".equals(envelope.optString("v")) ? "testSeen" : "seen") || pending.has(id) || pending.length() >= 16) return false;
                pending.put(id, envelope); state.put("pending", pending); write(state); return true;
            } catch (JSONException error) { throw new IOException("Invalid pending push"); }
        }
    }

    private static boolean seen(JSONObject state, String id) {
        return seen(state, id, "seen");
    }

    private static boolean seen(JSONObject state, String id, String key) {
        JSONArray seen = state.optJSONArray(key);
        if (seen != null) for (int i = 0; i < seen.length(); i++) if (id.equals(seen.optString(i))) return true;
        return false;
    }

    private static void remember(JSONObject state, String id) throws JSONException {
        remember(state, id, "seen");
    }

    private static void remember(JSONObject state, String id, String key) throws JSONException {
        JSONArray seen = state.optJSONArray(key); if (seen == null) seen = new JSONArray();
        if (!seen(state, id, key)) seen.put(id);
        while (seen.length() > 256) seen.remove(0);
        state.put(key, seen);
        JSONObject pending = state.optJSONObject("pending"); if (pending != null) pending.remove(id);
    }

    void drop(String id) throws IOException { update(state -> {
        JSONObject pending = state.optJSONObject("pending"), envelope = pending == null ? null : pending.optJSONObject(id);
        remember(state, id, envelope != null && "2".equals(envelope.optString("v")) ? "testSeen" : "seen");
    }); }

    void testReceipt(JSONObject envelope, JSONObject content, String stage) throws IOException {
        update(state -> {
            if (!matches(state, envelope, System.currentTimeMillis())) throw new JSONException("Binding changed");
            JSONObject queue = state.optJSONObject("testReceipts"); if (queue == null) queue = new JSONObject();
            for (Iterator<String> ids = queue.keys(); ids.hasNext();) {
                String key = ids.next();
                if (!matches(state, queue.getJSONObject(key).getJSONObject("envelope"), System.currentTimeMillis())) ids.remove();
            }
            String id = envelope.getString("deliveryId"); JSONObject item = queue.optJSONObject(id);
            if (item == null) {
                if (queue.length() >= 16) throw new JSONException("Receipt queue full");
                item = new JSONObject().put("envelope", envelope).put("receiptToken", content.getString("receiptToken"))
                        .put("stages", new JSONObject()); queue.put(id, item);
            }
            JSONObject stages = item.getJSONObject("stages");
            if (!stages.has(stage)) stages.put(stage, 0);
            state.put("testReceipts", queue);
        });
    }

    boolean commitTestDelivery(String id, Runnable display) throws IOException {
        synchronized (LOCK) {
            JSONObject state = read();
            try {
                JSONObject pending = state.optJSONObject("pending"), envelope = pending == null ? null : pending.optJSONObject(id);
                if (envelope == null || !"2".equals(envelope.optString("v")) || !matches(state, envelope, System.currentTimeMillis())
                        || seen(state, id, "testSeen")) return false;
                // Durable dedupe precedes notify. A crash cannot produce a second test banner.
                remember(state, id, "testSeen"); write(state); display.run(); return true;
            } catch (JSONException invalid) {throw new IOException("Invalid test delivery");}
        }
    }

    boolean commitDelivery(String id, JSONObject delivery, Runnable display) throws IOException {
        synchronized (LOCK) {
            JSONObject state = read();
            try {
                JSONObject pending = state.optJSONObject("pending");
                JSONObject envelope = pending == null ? null : pending.optJSONObject(id);
                if (envelope == null || !matches(state, envelope, System.currentTimeMillis()) || seen(state, id)) return false;
                JSONObject sequences = state.optJSONObject("sequences"); if (sequences == null) sequences = new JSONObject();
                String monitor = delivery.getString("monitorId"); long sequence = delivery.getLong("sequence");
                if (sequences.optLong(monitor) >= sequence) { remember(state, id); write(state); return false; }
                // Never evict a binding's watermark: that could revive a late DOWN after UP.
                sequences.put(monitor, sequence); state.put("sequences", sequences); remember(state, id); write(state);
                display.run(); return true;
            } catch (JSONException error) { throw new IOException("Invalid push delivery"); }
        }
    }
}
