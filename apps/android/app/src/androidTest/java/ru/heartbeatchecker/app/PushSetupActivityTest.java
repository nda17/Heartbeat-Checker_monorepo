package ru.heartbeatchecker.app;

import android.content.Context;
import android.content.Intent;
import android.Manifest;
import android.net.Uri;
import android.os.Build;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.TextView;

import androidx.test.core.app.ActivityScenario;
import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;

import org.json.JSONObject;
import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.IOException;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.List;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class PushSetupActivityTest {
    private static final String INSTALLATION = "123e4567-e89b-12d3-a456-426614174000";
    private static final String FRESH_INSTALLATION = "dddddddd-dddd-4ddd-8ddd-dddddddddddd";
    private static final String PAIRING = "987e6543-e21b-12d3-a456-426614174000";
    private static final String FRESH_PAIRING = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    private static final String CREDENTIAL = INSTALLATION + ".abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
    private static final String CODE = secret('A');
    private static final String CLIENT_STATE = secret('B');
    private static final String VERIFIER = secret('C');
    private static final String RETURN_URL = PushContract.ORIGIN + "/app/settings";
    private Context context;
    private PushStore store;

    @Before
    public void clearEncryptedStore() throws Exception {
        context = ApplicationProvider.getApplicationContext();
        store = new PushStore(context);
        PushWork.locallyBlocked = false;
        store.update(state -> {
            Iterator<String> keys = state.keys();
            List<String> remove = new ArrayList<>();
            while (keys.hasNext()) remove.add(keys.next());
            for (String key : remove) state.remove(key);
        });
    }

    @Test
    public void validBrowserReturnStoresCompletionCodeOnlyAfterMatchingLocalState() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, false, null);

        assertEquals(RETURN_URL, PushPairing.receive(context, PAIRING, CODE, CLIENT_STATE));
        JSONObject saved = store.read().getJSONObject("pair");
        assertEquals(CODE, saved.getString("completionCode"));
        assertEquals(CLIENT_STATE, saved.getString("clientState"));
        assertFalse("the native pairing state must not persist a browser session", store.read().has("cookies"));
    }

    @Test
    public void wrongPairWrongClientStateAndMissingCompletionCodeAreRejected() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, false, null);
        assertReceiveRejected("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", CODE, CLIENT_STATE);
        // A well-formed completion code cannot be judged locally; state mismatch is the native proof check.
        assertReceiveRejected(PAIRING, CODE, secret('D'));
        assertReceiveRejected(PAIRING, null, CLIENT_STATE);
        assertFalse("failed callbacks must not overwrite or add proof", store.read().getJSONObject("pair").has("completionCode"));
    }

    @Test
    public void consumedExchangeProofCannotBeOverwrittenByAnotherCallback() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, true, CODE);

        assertReceiveRejected(PAIRING, secret('D'), CLIENT_STATE);
        assertEquals(CODE, store.read().getJSONObject("pair").getString("completionCode"));
        assertTrue(store.read().getJSONObject("pair").getBoolean("exchangeAttempted"));
    }

    @Test
    public void oldIdOnlyCompletionUriAndIncompleteExtrasAreRejected() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, false, null);
        assertNull(PushContract.setupPair(PushContract.ORIGIN + "/android/push/complete?pairingId=" + PAIRING));
        Intent oldShape = callbackIntent().putExtra("pairingId", PAIRING);
        try (ActivityScenario<PushSetupActivity> scenario = ActivityScenario.launch(oldShape)) {
            assertEquals("ID-only callback is rejected and the handler closes", androidx.lifecycle.Lifecycle.State.DESTROYED,
                    scenario.getState());
        }
        assertFalse(store.read().getJSONObject("pair").has("completionCode"));
    }

    @Test
    public void externalConnectCannotClearOptOutOrStartPairing() throws Exception {
        store.update(state -> state.put("optedOut", true));
        try (ActivityScenario<PushSetupActivity> scenario = ActivityScenario.launch(connectIntent())) {
            scenario.onActivity(activity -> {
                assertButton(activity, "Включить уведомления");
                assertFalse(activity.isFinishing());
            });
        }
        assertTrue(store.read().getBoolean("optedOut"));
        assertFalse(store.read().has("pair"));
    }

    @Test
    public void mainBootstrapSkipsPairingWhenExplicitlyOptedOut() throws Exception {
        store.update(state -> state.put("optedOut", true));
        FakeTransport api = new FakeTransport();

        assertEquals(LinkPolicy.HOME, PushPairing.bootstrap(context, LinkPolicy.HOME, api));
        assertTrue("opt-out must short-circuit before any network or replacement installation", api.calls.isEmpty());
        assertTrue(store.read().getBoolean("optedOut"));
    }

    @Test
    public void expiredAmbiguousExchangeWithAuthoritativeMismatchStartsFreshPairing() throws Exception {
        seedPair(System.currentTimeMillis() - 1000, true, CODE);
        FakeTransport api = new FakeTransport();
        api.installationStatus = "pending";

        String next = PushPairing.bootstrap(context, RETURN_URL, api);

        assertEquals(PushContract.ORIGIN + "/app/settings?pushPair=" + FRESH_PAIRING, next);
        assertTrue(api.calls.get(0).startsWith("GET "));
        assertTrue(api.calls.get(1).startsWith("POST "));
        assertEquals("the consumed exchange is reconciled, never retried", 2, api.calls.size());
        JSONObject pending = store.read().getJSONObject("pair");
        assertFalse("a fresh bootstrap replaces the expired proof", pending.has("completionCode"));
        assertTrue(pending.has("verifier"));
        assertNotEquals("fresh bootstrap generates a new client state", CLIENT_STATE, pending.getString("clientState"));
    }

    @Test
    public void unresolvedUnexpiredAmbiguousExchangeBlocksFreshPairing() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, true, CODE);
        FakeTransport api = new FakeTransport();
        api.installationStatus = "pending";

        assertEquals(RETURN_URL, PushPairing.bootstrap(context, RETURN_URL, api));
        assertEquals("only the authoritative installation read is allowed", 1, api.calls.size());
        assertEquals(CODE, store.read().getJSONObject("pair").getString("completionCode"));
    }

    @Test
    public void serverFailurePreservesExistingCredentialAndPairing() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, false, null);
        FakeTransport api = new FakeTransport();
        api.failure = new PushApi.Failure(503);

        assertEquals(RETURN_URL, PushPairing.bootstrap(context, RETURN_URL, api));
        assertEquals(CREDENTIAL, store.read().getString("credential"));
        assertEquals(PAIRING, store.read().getJSONObject("pair").getString("id"));
        assertFalse(store.read().optBoolean("optedOut"));
    }

    @Test
    public void explicitEnableReplacesRemotelyRevokedCredentialOnFirstAttempt() throws Exception {
        store.update(state -> state.put("enabled", false).put("optedOut", true).put("credential", CREDENTIAL));
        FakeTransport api = new FakeTransport();
        api.getFailure = new PushApi.Failure(401);

        String next = PushPairing.enable(context, LinkPolicy.HOME, api);

        assertEquals(PushContract.ORIGIN + "/app/settings?pushPair=" + FRESH_PAIRING, next);
        assertTrue(api.calls.get(0).startsWith("GET "));
        assertTrue(api.calls.get(1).startsWith("POST "));
        assertEquals("one explicit action handles revoke and new bootstrap", 2, api.calls.size());
        JSONObject state = store.read();
        assertFalse(state.optBoolean("optedOut"));
        assertEquals(FRESH_INSTALLATION + ".abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", state.getString("credential"));
        assertEquals(FRESH_PAIRING, state.getJSONObject("pair").getString("id"));
    }

    @Test
    public void permissionRegrantAllowsExpiredAnonymousIdentityToBootstrapAfterRemoteRevoke() throws Exception {
        seedPair(System.currentTimeMillis() - 1000, false, null);
        store.update(state -> state.put("enabled", false).put("permissionPrompted", true)
                .put("installation", installation("pending", null)));
        grantNotificationPermission();
        assertTrue("the test must model the user granting notifications again", PushNotifications.permitted(context));
        FakeTransport api = new FakeTransport();
        api.getFailure = new PushApi.Failure(404);

        String next = PushPairing.bootstrap(context, RETURN_URL, api);

        assertEquals(PushContract.ORIGIN + "/app/settings?pushPair=" + FRESH_PAIRING, next);
        assertTrue(api.calls.get(0).startsWith("GET "));
        assertTrue(api.calls.get(1).startsWith("POST "));
        assertEquals("only a fresh anonymous pairing follows the expired orphan", 2, api.calls.size());
        JSONObject state = store.read();
        assertEquals(FRESH_INSTALLATION + ".abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", state.getString("credential"));
        assertEquals(FRESH_PAIRING, state.getJSONObject("pair").getString("id"));
        assertFalse(state.optBoolean("everExchangeAttempted"));
        assertFalse(state.optBoolean("optedOut"));
    }

    @Test
    public void ambiguousOrPreviouslyBoundPairCannotBeClassifiedAsAnExpiredAnonymousOrphan() throws Exception {
        seedPair(System.currentTimeMillis() - 1000, true, CODE);
        store.update(state -> state.put("enabled", false).put("everExchangeAttempted", true)
                .put("installation", installation("pending", null)));

        assertFalse(expiredAnonymous(store.read()));

        store.update(state -> state.put("everExchangeAttempted", false).put("installation",
                installation("session_revoked", PAIRING)));
        assertFalse("a prior pairing id must block anonymous recovery even at generation zero", expiredAnonymous(store.read()));

        store.update(state -> state.put("everExchangeAttempted", false).put("installation",
                installation("session_revoked", PAIRING).put("bindingGeneration", 1)));
        assertFalse("a formerly bound installation must not be recreated automatically", expiredAnonymous(store.read()));
    }

    @Test
    public void firstExchangeAttemptSetsStickyFenceAndFreshPairCannotClearIt() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, false, CODE);
        store.update(state -> state.put("token", "native-token"));
        grantNotificationPermission();
        FakeTransport exchange = new FakeTransport();
        exchange.exchangeFailure = new IOException("simulated lost exchange response");

        PushPairing.exchange(context, PAIRING, exchange);

        JSONObject attempted = store.read();
        assertTrue(attempted.optBoolean("everExchangeAttempted"));
        assertTrue(attempted.getJSONObject("pair").optBoolean("exchangeAttempted"));

        store.update(state -> state.getJSONObject("pair").put("expiresAt",
                Instant.ofEpochMilli(System.currentTimeMillis() - 1000).toString()));
        FakeTransport bootstrap = new FakeTransport();
        assertEquals(PushContract.ORIGIN + "/app/settings?pushPair=" + FRESH_PAIRING,
                PushPairing.bootstrap(context, RETURN_URL, bootstrap));
        JSONObject replaced = store.read();
        assertEquals(FRESH_PAIRING, replaced.getJSONObject("pair").getString("id"));
        assertTrue("replacement proof must retain the historical exchange fence",
                replaced.optBoolean("everExchangeAttempted"));
    }

    @Test
    public void persistedOptOutWithAmbiguousProofPreventsAnyAutomaticRequest() throws Exception {
        seedPair(System.currentTimeMillis() - 1000, true, CODE);
        store.update(state -> state.put("optedOut", true).put("everExchangeAttempted", true));
        FakeTransport api = new FakeTransport();

        assertEquals(RETURN_URL, PushPairing.bootstrap(context, RETURN_URL, api));
        assertTrue("sticky opt-out must short-circuit before any unsafe automatic recovery", api.calls.isEmpty());
        assertTrue(store.read().optBoolean("optedOut"));
        assertTrue(store.read().optBoolean("everExchangeAttempted"));
    }

    private void seedPair(long expiry, boolean attempted, String completionCode) throws Exception {
        store.update(state -> {
            JSONObject pair = new JSONObject().put("id", PAIRING).put("verifier", VERIFIER)
                    .put("clientState", CLIENT_STATE).put("expiresAt", Instant.ofEpochMilli(expiry).toString())
                    .put("confirmationUrl", PushContract.ORIGIN + "/app/settings?pushPair=" + PAIRING)
                    .put("returnUrl", RETURN_URL).put("exchangeAttempted", attempted);
            if (completionCode != null) pair.put("completionCode", completionCode);
            state.put("enabled", false).put("credential", CREDENTIAL).put("pair", pair);
        });
    }

    private static String secret(char value) {
        char[] chars = new char[43];
        java.util.Arrays.fill(chars, value);
        return new String(chars);
    }

    private void assertReceiveRejected(String id, String code, String state) throws Exception {
        try { PushPairing.receive(context, id, code, state); fail("invalid callback was accepted"); }
        catch (IllegalArgumentException expected) { }
    }

    private Intent connectIntent() {
        return new Intent(Intent.ACTION_VIEW, Uri.parse(PushContract.ORIGIN + "/android/push/connect"))
                .setClass(context, PushSetupActivity.class);
    }

    private Intent callbackIntent() {
        return new Intent(Intent.ACTION_VIEW, Uri.parse(PushContract.ORIGIN + "/android/push/complete"))
                .setClass(context, PushSetupActivity.class);
    }

    private static JSONObject installation(String status, String lastPairingId) throws org.json.JSONException {
        return new JSONObject().put("id", INSTALLATION).put("provider", "rustore").put("deviceLabel", "Test Android")
                .put("status", status).put("bindingGeneration", 0).put("registrationVersion", 1)
                .put("leaseExpiresAt", JSONObject.NULL).put("lastPairingId", lastPairingId == null ? JSONObject.NULL : lastPairingId)
                .put("lastSeenAt", Instant.now().toString());
    }

    private static boolean expiredAnonymous(JSONObject state) throws Exception {
        java.lang.reflect.Method method = PushPairing.class.getDeclaredMethod("expiredAnonymous", JSONObject.class, long.class);
        method.setAccessible(true);
        return (Boolean) method.invoke(null, state, System.currentTimeMillis());
    }

    private void grantNotificationPermission() throws Exception {
        if (Build.VERSION.SDK_INT >= 33 && context.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                != android.content.pm.PackageManager.PERMISSION_GRANTED) {
            InstrumentationRegistry.getInstrumentation().getUiAutomation()
                    .grantRuntimePermission(context.getPackageName(), Manifest.permission.POST_NOTIFICATIONS);
        }
    }

    private final class FakeTransport implements PushPairing.Transport {
        final List<String> calls = new ArrayList<>();
        String installationStatus = "pending";
        IOException failure;
        IOException getFailure;
        IOException exchangeFailure;
        final List<String> postCredentials = new ArrayList<>();

        @Override public JSONObject request(String method, String path, String credential, JSONObject body) throws IOException {
            calls.add(method + " " + path);
            if (failure != null) throw failure;
            if ("GET".equals(method)) {
                if (getFailure != null) throw getFailure;
                try { return installation(installationStatus, null); }
                catch (Exception e) { throw new IOException(e); }
            }
            if (path.endsWith("/exchange")) {
                if (exchangeFailure != null) throw exchangeFailure;
                try { return installation("pending", null); }
                catch (Exception e) { throw new IOException(e); }
            }
            try {
                assertEquals("POST", method);
                assertTrue(path.endsWith("/pairings"));
                postCredentials.add(credential == null ? "" : credential);
                assertTrue("fresh identities have no bearer credential", credential == null || credential.isEmpty()
                        || CREDENTIAL.equals(credential));
                assertTrue(PushContract.secret(body.getString("codeChallenge")));
                assertTrue(PushContract.secret(body.getString("clientState")));
                JSONObject pairing = new JSONObject().put("id", FRESH_PAIRING)
                        .put("expiresAt", Instant.ofEpochMilli(System.currentTimeMillis() + 300_000).toString())
                        .put("confirmationUrl", PushContract.ORIGIN + "/app/settings?pushPair=" + FRESH_PAIRING);
                JSONObject created = installation("pending", null);
                if (credential == null || credential.isEmpty()) {
                    created.put("id", FRESH_INSTALLATION).put("credential",
                            FRESH_INSTALLATION + ".abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG");
                }
                return new JSONObject().put("pairing", pairing).put("installation", created);
            } catch (Exception e) { throw new IOException(e); }
        }
    }

    private static void assertButton(PushSetupActivity activity, String expected) {
        if (findButton(activity.getWindow().getDecorView(), expected) == null)
            throw new AssertionError("expected visible button: " + expected);
    }

    private static Button findButton(View view, String expected) {
        if (view instanceof Button && expected.contentEquals(((Button) view).getText())) return (Button) view;
        if (view instanceof ViewGroup) {
            ViewGroup group = (ViewGroup) view;
            for (int i = 0; i < group.getChildCount(); i++) {
                Button found = findButton(group.getChildAt(i), expected);
                if (found != null) return found;
            }
        }
        return null;
    }
}
