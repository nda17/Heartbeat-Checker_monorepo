package ru.heartbeatchecker.app;

import android.content.Context;
import android.content.ContextWrapper;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import org.json.JSONException;
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
public final class PushNativeTestInstrumentationTest {
    private static final String ACCOUNT_A = "11111111-1111-4111-8111-111111111111";
    private static final String ACCOUNT_B = "33333333-3333-4333-8333-333333333333";
    private static final String SESSION_A = "22222222-2222-4222-8222-222222222222";
    private static final String SESSION_B = "44444444-4444-4444-8444-444444444444";
    private static final String INSTALL_A = "123e4567-e89b-12d3-a456-426614174000";
    private static final String INSTALL_B = "987e6543-e21b-12d3-a456-426614174000";
    private static final String SECRET_A = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
    private static final String SECRET_B = "BCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghi";
    private Context context;
    private File isolatedDirectory;
    private AuthStore auth;
    private PushStore push;
    private boolean previousLocallyBlocked;

    @Before
    public void setUp() throws Exception {
        previousLocallyBlocked = PushWork.locallyBlocked;
        Context app = ApplicationProvider.getApplicationContext();
        isolatedDirectory = new File(app.getNoBackupFilesDir(), "push-native-test-" + java.util.UUID.randomUUID());
        if (!isolatedDirectory.mkdirs() && !isolatedDirectory.isDirectory()) throw new IOException("create isolated test state");
        context = new ContextWrapper(app) {
            @Override public File getNoBackupFilesDir() { return isolatedDirectory; }
        };
        auth = new AuthStore(context);
        push = new PushStore(context);
        PushWork.locallyBlocked = false;
        auth.saveSession(session(ACCOUNT_A, SESSION_A), auth.epoch());
        push.update(state -> state.put("enabled", true).put("accountId", ACCOUNT_A).put("sessionId", SESSION_A)
                .put("credential", credential(INSTALL_A, SECRET_A)).put("installation", installation(INSTALL_A, 1)));
    }

    @After
    public void cleanUp() {
        PushWork.locallyBlocked = previousLocallyBlocked;
        deleteDirectory(isolatedDirectory);
    }

    @Test
    public void pendingTestScopeRequiresExactAccountSessionInstallationAndGeneration() throws Exception {
        JSONObject pending = pending("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ACCOUNT_A, SESSION_A, INSTALL_A, 1);
        JSONObject installation = installation(INSTALL_A, 1);
        assertTrue(PushNative.testScope(pending, ACCOUNT_A, SESSION_A, installation));
        assertFalse(PushNative.testScope(pending, ACCOUNT_A, SESSION_B, installation));
        assertFalse(PushNative.testScope(pending, ACCOUNT_B, SESSION_A, installation));
        assertFalse(PushNative.testScope(pending, ACCOUNT_A, SESSION_A, installation(INSTALL_B, 1)));
        assertFalse(PushNative.testScope(pending, ACCOUNT_A, SESSION_A, installation(INSTALL_A, 2)));
    }

    @Test
    public void staleReservationAfterLogoutAndAccountSwitchCannotReplaceNewOwnerRequest() throws Exception {
        long oldEpoch = auth.epoch();
        JSONObject oldInstallation = installation(INSTALL_A, 1);
        String oldCredential = credential(INSTALL_A, SECRET_A);
        String oldRequest = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
        PushNative.reserveTest(context, oldRequest, oldEpoch, ACCOUNT_A, SESSION_A, oldCredential, oldInstallation);
        assertEquals(oldRequest, push.read().getJSONObject("pendingTest").getString("requestId"));

        auth.logout();
        long newEpoch = auth.epoch();
        auth.saveSession(session(ACCOUNT_B, SESSION_B), newEpoch);
        String newCredential = credential(INSTALL_B, SECRET_B);
        String newRequest = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
        push.update(state -> state.put("enabled", true).put("accountId", ACCOUNT_B).put("sessionId", SESSION_B)
                .put("credential", newCredential).put("installation", installation(INSTALL_B, 1))
                .put("pendingTest", pending(newRequest, ACCOUNT_B, SESSION_B, INSTALL_B, 1)));

        try {
            PushNative.reserveTest(context, "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
                    oldEpoch, ACCOUNT_A, SESSION_A, oldCredential, oldInstallation);
            fail("stale owner was allowed to reserve a test after logout");
        } catch (IOException expected) { }

        JSONObject state = push.read();
        assertEquals("logout/account switch leaves the newer operation intact", newRequest,
                state.getJSONObject("pendingTest").getString("requestId"));
        assertEquals(ACCOUNT_B, state.getString("accountId"));
        assertEquals(SESSION_B, state.getString("sessionId"));
        assertTrue(auth.ownsPush(state));
    }

    private static JSONObject session(String account, String id) throws Exception {
        return new JSONObject().put("account", new JSONObject().put("id", account))
                .put("session", new JSONObject().put("id", id)
                        .put("token", SECRET_A).put("revokeToken", SECRET_B)
                        .put("expiresAt", Instant.ofEpochMilli(System.currentTimeMillis() + 3_600_000).toString()));
    }

    private static JSONObject installation(String id, long generation) throws JSONException {
        return new JSONObject().put("id", id).put("bindingGeneration", generation);
    }

    private static String credential(String id, String secret) { return id + "." + secret; }

    private static JSONObject pending(String request, String account, String session, String installation, long generation)
            throws JSONException {
        return new JSONObject().put("requestId", request).put("createdAt", System.currentTimeMillis())
                .put("accountId", account).put("sessionId", session).put("installationId", installation)
                .put("bindingGeneration", generation);
    }

    private static void deleteDirectory(File directory) {
        if (directory == null || !directory.exists()) return;
        File[] children = directory.listFiles();
        if (children != null) for (File child : children) deleteDirectory(child);
        directory.delete();
    }
}
