package ru.heartbeatchecker.app;

import android.content.Context;
import android.content.ContextWrapper;
import android.content.Intent;
import android.net.Uri;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.File;
import java.io.IOException;
import java.time.Instant;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class AuthStoreEmailVerificationTest {
    private static final String ACCOUNT = "11111111-1111-4111-8111-111111111111";
    private static final String SESSION_A = "22222222-2222-4222-8222-222222222222";
    private static final String SESSION_B = "33333333-3333-4333-8333-333333333333";
    private static final String TOKEN_A = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
    private static final String TOKEN_B = "BCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefgh";
    private Context context;
    private File directory;
    private AuthStore store;

    @Before
    public void setUp() throws Exception {
        Context app = ApplicationProvider.getApplicationContext();
        directory = new File(app.getNoBackupFilesDir(), "auth-email-test-" + java.util.UUID.randomUUID());
        if (!directory.mkdirs() && !directory.isDirectory()) throw new IOException("create isolated auth state");
        context = new ContextWrapper(app) {
            @Override public File getNoBackupFilesDir() { return directory; }
        };
        store = new AuthStore(context);
    }

    @After
    public void tearDown() {
        deleteDirectory(directory);
    }

    @Test
    public void onlyExactHttpsVerificationFragmentIsAccepted() {
        String link = "https://heartbeat-checker.ru/app/settings#verify-email=" + TOKEN_A;
        assertEquals(link, normalize(link));
        assertEquals(TOKEN_A, MainActivity.emailVerificationToken(link));

        for (String rejected : new String[] {
                "http://heartbeat-checker.ru/app/settings#verify-email=" + TOKEN_A,
                "https://heartbeat-checker.ru/app/settings?next=1#verify-email=" + TOKEN_A,
                "https://heartbeat-checker.ru/app/settings#verify-email=" + TOKEN_A + "&extra=1",
                "https://heartbeat-checker.ru/app/settings#%76erify-email=" + TOKEN_A,
                "https://heartbeat-checker.ru/app/settings#verify-email=%41" + TOKEN_A.substring(1),
                "https://heartbeat-checker.ru/app/settings#verify-email=" + TOKEN_A + "=",
                "https://heartbeat-checker.ru/app/settings/extra#verify-email=" + TOKEN_A,
        }) {
            assertNull("noncanonical verification URL was accepted", normalize(rejected));
            assertNull("noncanonical verification URL exposed a token", MainActivity.emailVerificationToken(rejected));
        }
    }

    @Test
    public void unauthenticatedLinkSurvivesStoreRestartAndBindsOnLogin() throws Exception {
        JSONObject pending = store.emailVerification(TOKEN_A, false);
        assertNotNull(pending);
        long originalReceivedAt = pending.getLong("receivedAt");
        assertTrue("an unauthenticated link stores an explicit accountId null", pending.has("accountId")
                && pending.isNull("accountId"));
        assertTrue("an unauthenticated link stores an explicit sessionId null", pending.has("sessionId")
                && pending.isNull("sessionId"));

        AuthStore restarted = new AuthStore(context);
        JSONObject restored = restarted.emailVerification(null, false);
        assertEquals("pending token survives process restart", TOKEN_A, restored.getString("token"));
        assertEquals(originalReceivedAt, restored.getLong("receivedAt"));
        assertTrue("the restarted pending link is still unbound to an account",
                restored.has("accountId") && restored.isNull("accountId"));
        assertTrue("the restarted pending link is still unbound to a session",
                restored.has("sessionId") && restored.isNull("sessionId"));

        long epoch = restarted.epoch();
        restarted.saveSession(session(ACCOUNT, SESSION_A), epoch);
        JSONObject bound = restarted.emailVerification(null, false);
        assertEquals(ACCOUNT, bound.getString("accountId"));
        assertEquals(SESSION_A, bound.getString("sessionId"));
        assertEquals("replaying same incoming intent must not reset receipt time", originalReceivedAt,
                restarted.emailVerification(TOKEN_A, false).getLong("receivedAt"));
    }

    @Test
    public void verificationPendingForAnotherSessionIsClearedWithoutExposure() throws Exception {
        store.emailVerification(TOKEN_A, false);
        store.saveSession(session(ACCOUNT, SESSION_A), store.epoch());

        // A subsequent login for the same account creates a different native session.
        store.saveSession(session(ACCOUNT, SESSION_B), store.epoch());
        assertNull("old session's email token must not be returned to the new session",
                store.emailVerification(null, false));
        assertFalse("foreign pending token is removed", store.read().has("emailVerification"));
    }

    @Test
    public void clearingAnOlderConsumedLinkCannotEraseANewerLink() throws Exception {
        store.emailVerification(TOKEN_A, false);
        store.emailVerification(TOKEN_B, false);
        store.emailVerification(TOKEN_A, true);
        JSONObject current = store.emailVerification(null, false);
        assertEquals("late cleanup is conditional on exact token", TOKEN_B, current.getString("token"));
        store.emailVerification(TOKEN_B, true);
        assertNull(store.emailVerification(null, false));
    }

    @Test
    public void expiredLinkLeavesOnlyNonrenewableDigestTombstone() throws Exception {
        store.emailVerification(TOKEN_A, false);
        JSONObject expired = new JSONObject().put("token", TOKEN_A)
                .put("receivedAt", System.currentTimeMillis() - 30 * 60_000L - 1)
                .put("accountId", JSONObject.NULL).put("sessionId", JSONObject.NULL);
        store.put("emailVerification", expired);

        assertNull("expired link is not restored", store.emailVerification(null, false));
        JSONObject tombstone = store.read().getJSONObject("emailVerification");
        assertFalse("expired plaintext token is discarded", tombstone.has("token"));
        assertTrue("only a token digest remains to stop replay from renewing TTL", tombstone.has("tokenDigest"));
        assertNull("the same intent cannot restart an expired verification link", store.emailVerification(TOKEN_A, false));
        assertFalse(store.read().getJSONObject("emailVerification").has("token"));
    }

    private static String normalize(String value) {
        return MainActivity.normalize(new Intent(Intent.ACTION_VIEW, Uri.parse(value)));
    }

    private static JSONObject session(String account, String id) throws Exception {
        return new JSONObject().put("account", new JSONObject().put("id", account))
                .put("session", new JSONObject().put("id", id).put("token", TOKEN_A).put("revokeToken", TOKEN_B)
                        .put("expiresAt", Instant.ofEpochMilli(System.currentTimeMillis() + 3_600_000).toString()));
    }

    private static void deleteDirectory(File directory) {
        if (directory == null || !directory.exists()) return;
        File[] children = directory.listFiles();
        if (children != null) for (File child : children) deleteDirectory(child);
        directory.delete();
    }
}
