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
import java.security.MessageDigest;
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
    private static final long EMAIL_VERIFICATION_TTL = 30 * 60_000;
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
                JSONObject email = state.optJSONObject("emailVerification");
                if (email != null) {
                    if (!validEmailVerification(email, System.currentTimeMillis())) expireEmailVerification(state, email);
                    else if (email.isNull("accountId") && email.isNull("sessionId"))
                        bindEmailVerification(email, auth);
                    else if (!emailScope(email, auth)) state.remove("emailVerification");
                }
                state.put("auth", auth).put("gate", true).put("epoch", state.optLong("epoch") + 1);
                state.remove("registration");
                if (previous == null || !previous.getJSONObject("session").getString("id").equals(auth.getJSONObject("session").getString("id")))
                    state.remove("monitorDraft");
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
                state.remove("registration"); state.remove("paymentIntent"); state.remove("monitorDraft");
                state.remove("emailVerification"); write(state);
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

    JSONObject monitorDraft(String accountId, String sessionId, JSONObject value, boolean clear) throws IOException, JSONException {
        synchronized (LOCK) {
            JSONObject auth = active();
            if (auth == null || !PushContract.uuid(accountId) || !PushContract.uuid(sessionId)
                    || !accountId.equals(auth.getJSONObject("account").getString("id"))
                    || !sessionId.equals(auth.getJSONObject("session").getString("id"))) throw new IOException("Session changed");
            JSONObject state = read();
            if (clear) {state.remove("monitorDraft"); write(state); return null;}
            if (value != null) {
                if (value.toString().getBytes(StandardCharsets.UTF_8).length > 16384 || value.optInt("schemaVersion") != 1
                        || value.optJSONObject("form") == null) throw new JSONException("Invalid draft");
                state.put("monitorDraft", new JSONObject().put("accountId", accountId).put("sessionId", sessionId).put("value", value));
                write(state); return null;
            }
            JSONObject saved = state.optJSONObject("monitorDraft");
            return saved != null && accountId.equals(saved.optString("accountId")) && sessionId.equals(saved.optString("sessionId"))
                    ? saved.optJSONObject("value") : null;
        }
    }

    private static String emailTokenDigest(String token) throws IOException {
        try {
            return android.util.Base64.encodeToString(MessageDigest.getInstance("SHA-256").digest(token.getBytes(StandardCharsets.UTF_8)), android.util.Base64.NO_WRAP);
        } catch (Exception failure) {throw new IOException("Email verification unavailable");}
    }

    private static boolean validEmailVerification(JSONObject value, long now) {
        Object received = value.opt("receivedAt");
        if (!(received instanceof Long || received instanceof Integer) || !PushContract.secret(value.optString("token"))) return false;
        long at = ((Number) received).longValue();
        return at > 0 && at <= now && now - at < EMAIL_VERIFICATION_TTL
                && (value.isNull("accountId") && value.isNull("sessionId")
                || PushContract.uuid(value.optString("accountId")) && PushContract.uuid(value.optString("sessionId")));
    }

    private static boolean emailScope(JSONObject pending, JSONObject auth) {
        return auth != null && pending.optString("accountId").equals(auth.optJSONObject("account").optString("id"))
                && pending.optString("sessionId").equals(auth.optJSONObject("session").optString("id"));
    }

    private static void bindEmailVerification(JSONObject pending, JSONObject auth) throws JSONException {
        pending.put("accountId", auth.getJSONObject("account").getString("id"))
                .put("sessionId", auth.getJSONObject("session").getString("id"));
    }

    private static void expireEmailVerification(JSONObject state, JSONObject previous) throws JSONException, IOException {
        // Retain only a digest tombstone: replay of the same cold/warm intent must not restart its TTL.
        String digest = previous.optString("tokenDigest");
        if (PushContract.secret(previous.optString("token"))) digest = emailTokenDigest(previous.getString("token"));
        state.put("emailVerification", new JSONObject().put("tokenDigest", digest).put("receivedAt", previous.optLong("receivedAt")));
    }

    JSONObject emailVerification(String token, boolean clear) throws IOException, JSONException {
        synchronized (LOCK) {
            JSONObject auth = active(), state = read(), pending = state.optJSONObject("emailVerification");
            if (clear) {
                if (pending != null && token != null && (token.equals(pending.optString("token"))
                        || emailTokenDigest(token).equals(pending.optString("tokenDigest")))) {
                    state.remove("emailVerification"); write(state);
                }
                return null;
            }
            long now = System.currentTimeMillis();
            if (token != null) {
                if (!PushContract.secret(token)) throw new JSONException("Invalid verification token");
                if (pending != null && emailTokenDigest(token).equals(pending.optString("tokenDigest"))) return null;
                if (pending == null || !token.equals(pending.optString("token")))
                    pending = new JSONObject().put("token", token).put("receivedAt", now).put("accountId", JSONObject.NULL).put("sessionId", JSONObject.NULL);
            }
            if (pending == null) return null;
            if (!validEmailVerification(pending, now)) {
                expireEmailVerification(state, pending); write(state); return null;
            }
            if (pending.isNull("accountId") && pending.isNull("sessionId")) {
                if (auth != null) bindEmailVerification(pending, auth);
            } else if (!emailScope(pending, auth)) {
                state.remove("emailVerification"); write(state); return null;
            }
            state.put("emailVerification", pending); write(state);
            return new JSONObject(pending.toString());
        }
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
