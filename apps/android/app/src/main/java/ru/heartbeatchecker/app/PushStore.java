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

    PushStore(Context context) { file = new AtomicFile(new File(context.getNoBackupFilesDir(), "push-state.enc")); }

    boolean nearLimit() { return file.getBaseFile().length() > LIMIT - 8192; }

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
        update(state -> {
            if (!expectedCredential.equals(state.optString("credential"))) return;
            JSONObject previous = state.optJSONObject("installation");
            if (previous != null && (installation.getLong("bindingGeneration") < previous.optLong("bindingGeneration")
                    || installation.getLong("registrationVersion") < previous.optLong("registrationVersion")))
                throw new JSONException("Stale installation response");
            if (previous == null || previous.optLong("bindingGeneration") != installation.getLong("bindingGeneration")) {
                state.remove("pending"); state.remove("seen"); state.remove("sequences");
            }
            state.put("installation", installation);
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
                if (seen(state, id) || pending.has(id) || pending.length() >= 16) return false;
                pending.put(id, envelope); state.put("pending", pending); write(state); return true;
            } catch (JSONException error) { throw new IOException("Invalid pending push"); }
        }
    }

    private static boolean seen(JSONObject state, String id) {
        JSONArray seen = state.optJSONArray("seen");
        if (seen != null) for (int i = 0; i < seen.length(); i++) if (id.equals(seen.optString(i))) return true;
        return false;
    }

    private static void remember(JSONObject state, String id) throws JSONException {
        JSONArray seen = state.optJSONArray("seen"); if (seen == null) seen = new JSONArray();
        if (!seen(state, id)) seen.put(id);
        while (seen.length() > 256) seen.remove(0);
        state.put("seen", seen);
        JSONObject pending = state.optJSONObject("pending"); if (pending != null) pending.remove(id);
    }

    void drop(String id) throws IOException { update(state -> remember(state, id)); }

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
