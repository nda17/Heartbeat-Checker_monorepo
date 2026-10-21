package ru.heartbeatchecker.app;

import android.content.Context;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.File;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.List;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class NativeAuthStoreTest {
    private static final String ACCOUNT_A = "11111111-1111-4111-8111-111111111111";
    private static final String ACCOUNT_B = "33333333-3333-4333-8333-333333333333";
    private static final String SESSION_A = "22222222-2222-4222-8222-222222222222";
    private static final String SESSION_B = "44444444-4444-4444-8444-444444444444";
    private static final String TOKEN = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
    private static final String REVOKE = "BCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghi";
    private Context context;
    private AuthStore store;

    @Before
    public void clearPriorNativeState() throws Exception {
        context = ApplicationProvider.getApplicationContext();
        store = new AuthStore(context);
        clearStore();
    }

    @After
    public void clearNativeStateAfterTest() throws Exception {
        clearStore();
    }

    @Test
    public void encryptedSessionHasDurableGateOwnerFenceAndRevokeOnlyLogoutQueue() throws Exception {
        JSONObject first = session(ACCOUNT_A, SESSION_A);
        store.saveSession(first, store.epoch());
        assertEquals(ACCOUNT_A, store.active().getJSONObject("account").getString("id"));
        assertEquals(1, store.epoch());

        JSONObject push = new JSONObject().put("accountId", ACCOUNT_A).put("sessionId", SESSION_A);
        assertTrue("push is owned only by the exact active account and session", store.ownsPush(push));
        assertFalse("an account match cannot authorize a different session", store.ownsPush(
                new JSONObject().put("accountId", ACCOUNT_A).put("sessionId", SESSION_B)));
        assertFalse("a session match cannot authorize a different account", store.ownsPush(
                new JSONObject().put("accountId", ACCOUNT_B).put("sessionId", SESSION_A)));

        File encrypted = new File(context.getNoBackupFilesDir(), "auth-state.enc");
        byte[] ciphertext = Files.readAllBytes(encrypted.toPath());
        assertEquals("versioned encrypted state", 1, ciphertext[0]);
        String raw = new String(ciphertext, StandardCharsets.UTF_8);
        assertFalse(raw.contains(TOKEN));
        assertFalse(raw.contains(REVOKE));
        assertFalse(raw.contains(SESSION_A));
        assertEquals("a new store instance can decrypt the committed session", ACCOUNT_A,
                new AuthStore(context).active().getJSONObject("account").getString("id"));

        try {
            store.saveSession(session(ACCOUNT_B, SESSION_B), store.epoch());
            fail("an account switch bypassed the durable logout fence");
        } catch (java.io.IOException expected) { }
        assertEquals(ACCOUNT_A, store.active().getJSONObject("account").getString("id"));

        store.logout();
        assertNull("logout drops active native authorization before background work starts", store.active());
        assertFalse(store.read().optBoolean("gate"));
        assertEquals("logout advances the epoch to invalidate in-flight work", 2, store.epoch());
        assertEquals(1, store.pendingRevokes().length());
        JSONObject pending = store.pendingRevokes().getJSONObject(0);
        assertEquals(SESSION_A, pending.getString("sessionId"));
        assertEquals(REVOKE, pending.getString("revokeToken"));
        assertFalse("logout does not queue an access bearer", pending.has("token"));
        assertFalse("push ownership is revoked immediately even while offline", store.ownsPush(push));

        try {
            store.saveSession(session(ACCOUNT_B, SESSION_B), 1);
            fail("a delayed login response reopened the auth gate with a stale epoch");
        } catch (java.io.IOException expected) { }
        assertFalse("the epoch compare-and-set leaves logout closed", store.read().optBoolean("gate"));
        assertEquals(1, store.pendingRevokes().length());

        store.saveSession(session(ACCOUNT_B, SESSION_B), store.epoch());
        assertEquals("account switching becomes available after local logout", ACCOUNT_B,
                store.active().getJSONObject("account").getString("id"));
        assertEquals("revocation work remains queued independently", 1, store.pendingRevokes().length());
    }

    @Test
    public void sameSessionOAuthRotationKeepsRevokeCapabilityAndExpiryFailsClosed() throws Exception {
        JSONObject initial = session(ACCOUNT_A, SESSION_A);
        store.saveSession(initial, store.epoch());
        String revoke = initial.getJSONObject("session").getString("revokeToken");
        JSONObject rotated = session(ACCOUNT_A, SESSION_A);
        rotated.getJSONObject("session").put("token", "new-access-token-abcdefghijklmnopqrstuvwxyz0123456789").remove("revokeToken");
        store.saveSession(rotated, store.epoch());
        JSONObject current = store.active();
        assertNotNull(current);
        assertEquals("same SID/account rotation inherits the previous revoke-only capability", revoke,
                current.getJSONObject("session").getString("revokeToken"));

        store.logout(); // Seed real offline revoke work that expiry cleanup must preserve.
        store.saveSession(session(ACCOUNT_A, SESSION_A), store.epoch());
        JSONObject expired = session(ACCOUNT_A, SESSION_A);
        expired.getJSONObject("session").put("expiresAt", Instant.ofEpochMilli(System.currentTimeMillis() - 1_000).toString());
        store.put("auth", expired);
        long before = store.epoch();
        assertNull("expired auth never restores an active account", store.active());
        assertFalse(store.read().optBoolean("gate"));
        assertEquals("expired auth closes the gate and fences stale responses", before + 1, store.epoch());
        assertEquals("expiry cleanup preserves prior revoke work without adding expired credentials", 1, store.pendingRevokes().length());
        store.saveSession(session(ACCOUNT_B, SESSION_B), store.epoch());
        assertEquals(ACCOUNT_B, store.active().getJSONObject("account").getString("id"));
        assertEquals("revoke-only queue survives account replacement", 1, store.pendingRevokes().length());
    }

    private void clearStore() throws Exception {
        JSONObject state = store.read();
        Iterator<String> keys = state.keys();
        List<String> names = new ArrayList<>();
        while (keys.hasNext()) names.add(keys.next());
        for (String key : names) store.remove(key);
    }

    private static JSONObject session(String accountId, String sessionId) throws Exception {
        return new JSONObject().put("account", new JSONObject().put("id", accountId))
                .put("session", new JSONObject().put("id", sessionId).put("token", TOKEN).put("revokeToken", REVOKE)
                        .put("expiresAt", Instant.ofEpochMilli(System.currentTimeMillis() + 3_600_000).toString()));
    }
}
