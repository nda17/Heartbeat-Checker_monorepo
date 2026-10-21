package ru.heartbeatchecker.app;

import android.content.Context;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.AtomicFile;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileNotFoundException;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.util.Arrays;

import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/** Bounded, atomic, no-backup session state. A damaged store always fails closed. */
final class AuthStore {
    private static final Object LOCK = new Object();
    private static final String ALIAS = "heartbeat.auth.v1";
    private static final int LIMIT = 64 * 1024;
    private final AtomicFile file;

    AuthStore(Context context) { file = new AtomicFile(new File(context.getNoBackupFilesDir(), "auth-state.enc")); }

    private SecretKey key() throws Exception {
        KeyStore store = KeyStore.getInstance("AndroidKeyStore"); store.load(null);
        if (store.containsAlias(ALIAS)) return (SecretKey) store.getKey(ALIAS, null);
        KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
        generator.init(new KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true).build());
        return generator.generateKey();
    }

    JSONObject read() throws IOException {
        synchronized (LOCK) {
            try {
                byte[] bytes;
                try (java.io.InputStream input = file.openRead(); ByteArrayOutputStream output = new ByteArrayOutputStream()) {
                    byte[] buffer = new byte[4096]; int count;
                    while ((count = input.read(buffer)) != -1) {
                        if (output.size() + count > LIMIT + 64) throw new IOException("Auth state limit");
                        output.write(buffer, 0, count);
                    }
                    bytes = output.toByteArray();
                }
                if (bytes.length < 30 || bytes[0] != 1) throw new IOException("Invalid auth state");
                Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
                cipher.init(Cipher.DECRYPT_MODE, key(), new GCMParameterSpec(128, Arrays.copyOfRange(bytes, 1, 13)));
                return new JSONObject(new String(cipher.doFinal(Arrays.copyOfRange(bytes, 13, bytes.length)), StandardCharsets.UTF_8));
            } catch (FileNotFoundException absent) {
                if (file.getBaseFile().exists()) throw new IOException("Auth state unavailable");
                return new JSONObject();
            } catch (Exception failure) { throw new IOException("Auth state unavailable"); }
        }
    }

    private void write(JSONObject value) throws IOException {
        FileOutputStream output = null;
        try {
            byte[] body = value.toString().getBytes(StandardCharsets.UTF_8);
            if (body.length > LIMIT) throw new IOException("Auth state limit");
            Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding"); cipher.init(Cipher.ENCRYPT_MODE, key());
            byte[] encrypted = cipher.doFinal(body);
            output = file.startWrite(); output.write(1); output.write(cipher.getIV()); output.write(encrypted); file.finishWrite(output);
        } catch (Exception failure) {
            if (output != null) file.failWrite(output);
            throw new IOException("Auth state unavailable");
        }
    }

    private static boolean valid(JSONObject data) {
        try {
            JSONObject account = data.getJSONObject("account"); JSONObject session = data.getJSONObject("session");
            long expiresAt = PushContract.instant(session.getString("expiresAt")), now = System.currentTimeMillis();
            return PushContract.uuid(account.getString("id")) && PushContract.uuid(session.getString("id"))
                    && session.getString("token").length() >= 32 && session.getString("revokeToken").length() >= 32
                    && expiresAt > now && expiresAt <= now + 30L * 24 * 60 * 60_000 + 5 * 60_000;
        } catch (Exception ignored) { return false; }
    }

    JSONObject active() throws IOException {
        synchronized (LOCK) {
            JSONObject state = read(); JSONObject value = state.optJSONObject("auth");
            if (!state.optBoolean("gate")) return null;
            if (value != null && valid(value)) return value;
            // Expired or malformed auth must durably close its gate before the login UI is shown.
            logout();
            return null;
        }
    }

    long epoch() throws IOException { return read().optLong("epoch"); }

    void saveSession(JSONObject auth, long expectedEpoch) throws IOException {
        synchronized (LOCK) {
            JSONObject state = read(); JSONObject previous = state.optJSONObject("auth");
            if (state.optLong("epoch") != expectedEpoch) throw new IOException("Session changed");
            try {
                JSONObject incomingSession = auth.getJSONObject("session");
                JSONObject oldSession = previous == null ? null : previous.optJSONObject("session");
                boolean sameSession = state.optBoolean("gate") && previous != null && valid(previous)
                        && previous.getJSONObject("account").getString("id").equals(auth.getJSONObject("account").getString("id"))
                        && oldSession.getString("id").equals(incomingSession.getString("id"));
                if (!incomingSession.has("revokeToken")) {
                    if (!sameSession) throw new IOException("Missing revocation capability");
                    incomingSession.put("revokeToken", oldSession.getString("revokeToken"));
                } else if (sameSession && !oldSession.getString("revokeToken").equals(incomingSession.getString("revokeToken"))) {
                    throw new IOException("Revocation capability changed");
                }
            } catch (JSONException failure) {throw new IOException("Invalid native session");}
            if (!valid(auth)) throw new IOException("Invalid native session");
            if (previous != null && !previous.optJSONObject("account").optString("id").equals(auth.optJSONObject("account").optString("id"))) {
                // Account switch must first pass a durable local logout.
                throw new IOException("Existing account must sign out");
            }
            try {
                state.put("auth", auth).put("gate", true).put("epoch", state.optLong("epoch") + 1);
                state.remove("registration");
                write(state);
            } catch (JSONException failure) { throw new IOException("Invalid native session"); }
        }
    }

    void logout() throws IOException {
        synchronized (LOCK) {
            JSONObject state = read(); JSONObject auth = state.optJSONObject("auth");
            try {
                JSONArray pending = state.optJSONArray("pendingRevokes"); if (pending == null) pending = new JSONArray();
                if (auth != null && valid(auth)) {
                    JSONObject session = auth.getJSONObject("session");
                    pending.put(new JSONObject().put("sessionId", session.getString("id"))
                            .put("revokeToken", session.getString("revokeToken")).put("expiresAt", session.getString("expiresAt")));
                }
                // The gate is persisted before any queue worker or push cleanup runs.
                state.put("gate", false).put("epoch", state.optLong("epoch") + 1).put("pendingRevokes", pending);
                state.remove("auth"); state.remove("oauth"); state.remove("oauthRegistration");
                state.remove("registration"); state.remove("paymentIntent"); write(state);
            } catch (JSONException failure) { throw new IOException("Invalid revocation state"); }
        }
    }

    void put(String key, JSONObject value) throws IOException {
        synchronized (LOCK) {
            JSONObject state = read();
            try { state.put(key, value); write(state); }
            catch (JSONException failure) { throw new IOException("Invalid auth state"); }
        }
    }
    void remove(String key) throws IOException {
        synchronized (LOCK) { JSONObject state = read(); state.remove(key); write(state); }
    }

    void removeOAuth(String flowId, String oauthState) throws IOException {
        synchronized (LOCK) {
            JSONObject state = read(); JSONObject pending = state.optJSONObject("oauth");
            if (pending == null || !flowId.equals(pending.optString("flowId"))
                    || !oauthState.equals(pending.optString("state"))) return;
            state.remove("oauth"); write(state);
        }
    }

    boolean ownsPush(JSONObject push) {
        try {
            JSONObject active = active();
            return active != null && active.getJSONObject("account").getString("id").equals(push.optString("accountId"))
                    && active.getJSONObject("session").getString("id").equals(push.optString("sessionId"));
        } catch (Exception ignored) { return false; }
    }

    JSONArray pendingRevokes() throws IOException { JSONArray items = read().optJSONArray("pendingRevokes"); return items == null ? new JSONArray() : items; }
    void finishRevoke(String id) throws IOException {
        synchronized (LOCK) {
            JSONObject state = read(); JSONArray queue = state.optJSONArray("pendingRevokes"); if (queue == null) return;
            for (int i = queue.length() - 1; i >= 0; i--) if (id.equals(queue.optJSONObject(i).optString("sessionId"))) queue.remove(i);
            try { state.put("pendingRevokes", queue); write(state); } catch (JSONException failure) { throw new IOException("Invalid revocation queue"); }
        }
    }
}
