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
import androidx.work.ListenableWorker;

import org.json.JSONObject;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.IOException;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Iterator;
import java.util.List;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

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

    @After
    public void restoreLocalBlock() {
        PushWork.locallyBlocked = false;
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
        assertEquals("an ambiguous POST is reconciled by one authoritative GET", 2, exchange.calls.size());
        assertTrue(exchange.calls.get(0).startsWith("POST "));
        assertTrue(exchange.calls.get(1).startsWith("GET "));

        JSONObject attempted = store.read();
        assertTrue(attempted.optBoolean("everExchangeAttempted"));
        assertTrue(attempted.getJSONObject("pair").optBoolean("exchangeAttempted"));

        FakeTransport retry = new FakeTransport();
        PushPairing.exchange(context, PAIRING, retry);
        assertEquals("a later worker retry reconciles with GET and never repeats the consumed POST", 1, retry.calls.size());
        assertTrue(retry.calls.get(0).startsWith("GET "));

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
    public void canceledOrSupersededWorkerCannotExchangeThePersistedProof() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, false, CODE);
        store.update(state -> state.put("token", "a-valid-native-token-1234567890"));
        grantNotificationPermission();
        FakeTransport api = new FakeTransport();

        PushPairing.exchange(context, PAIRING, api, PushWork.localRevocation, () -> false);
        assertTrue("a stopped worker sends no request", api.calls.isEmpty());
        assertFalse(store.read().getJSONObject("pair").optBoolean("exchangeAttempted"));
        assertFalse(store.read().optBoolean("everExchangeAttempted"));

        PushPairing.exchange(context, PAIRING, api, PushWork.localRevocation + 1, () -> true);
        assertTrue("a pairing invalidated by local revocation sends no request", api.calls.isEmpty());
        PushPairing.exchange(context, FRESH_PAIRING, api);
        assertTrue("a worker for a replaced pairing sends no request", api.calls.isEmpty());
    }

    @Test
    public void immediateLocalDisableFencesAnAlreadyQueuedPairingWorker() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, false, CODE);
        PushWork.locallyBlocked = true;
        AtomicInteger sdkCalls = new AtomicInteger();
        AtomicInteger exchangeCalls = new AtomicInteger();
        PushWorker.PairingServices forbidden = new PushWorker.PairingServices() {
            @Override public void token(PushSdk.TokenResult result) { sdkCalls.incrementAndGet(); }
            @Override public void exchange(String id, long revocation, java.util.function.BooleanSupplier active) {
                exchangeCalls.incrementAndGet();
            }
        };

        ListenableWorker.Result result = PushWorker.runPairing(context, PAIRING, () -> false, forbidden, 1000);

        assertEquals(ListenableWorker.Result.success().getClass(), result.getClass());
        assertEquals("disabled state blocks provider calls", 0, sdkCalls.get());
        assertEquals("disabled state blocks network calls", 0, exchangeCalls.get());
        assertEquals("the durable proof remains available for explicit reconnect", CODE,
                store.read().getJSONObject("pair").getString("completionCode"));
    }

    @Test
    public void delayedProviderTokenAfterSetupDeadlineLeavesDurableProofForWorkerRetry() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, false, CODE);
        AtomicReference<PushSdk.TokenResult> delayedCallback = new AtomicReference<>();

        assertFalse("a stalled provider token must not hold setup open",
                PushWorker.awaitToken(10, () -> true, delayedCallback::set));
        assertNotNull(delayedCallback.get());
        delayedCallback.get().ready("a-valid-native-token-1234567890");

        JSONObject afterLateCallback = store.read();
        assertEquals("the completion proof remains persisted after the UI deadline", CODE,
                afterLateCallback.getJSONObject("pair").getString("completionCode"));
        assertFalse("a token arriving after timeout cannot mark an exchange as attempted",
                afterLateCallback.getJSONObject("pair").optBoolean("exchangeAttempted"));
        assertFalse("the sticky exchange fence remains clear until a request is actually sent",
                afterLateCallback.optBoolean("everExchangeAttempted"));

        assertTrue("a resumed worker can still use a later token",
                PushWorker.awaitToken(1000, () -> true, callback -> callback.ready("a-valid-native-token-1234567890")));
        assertEquals(CODE, store.read().getJSONObject("pair").getString("completionCode"));
    }

    @Test
    public void pairingWorkerKeepsProofAcrossTokenTimeoutAndNeverRepostsAmbiguousExchange() throws Exception {
        seedPair(System.currentTimeMillis() + 300_000, false, CODE);
        grantNotificationPermission();
        AtomicReference<PushSdk.TokenResult> delayedToken = new AtomicReference<>();
        AtomicInteger exchangesBeforeRetry = new AtomicInteger();
        PushWorker.PairingServices stalled = new PushWorker.PairingServices() {
            @Override public void token(PushSdk.TokenResult result) { delayedToken.set(result); }
            @Override public void exchange(String id, long revocation, java.util.function.BooleanSupplier active) {
                exchangesBeforeRetry.incrementAndGet();
            }
        };

        ListenableWorker.Result timedOut = PushWorker.runPairing(context, PAIRING, () -> false, stalled, 25);
        assertEquals(ListenableWorker.Result.retry().getClass(), timedOut.getClass());
        assertEquals(0, exchangesBeforeRetry.get());
        assertEquals("the pairing proof survives a worker token timeout", CODE,
                store.read().getJSONObject("pair").getString("completionCode"));
        assertEquals(1, store.read().getJSONObject("pair").getInt("workerAttempts"));
        assertFalse(store.read().optBoolean("everExchangeAttempted"));

        assertNotNull(delayedToken.get());
        delayedToken.get().ready("native-token-1234567890ABCDEF");
        assertEquals("a late SDK callback cannot start an exchange after the worker has timed out", 0,
                exchangesBeforeRetry.get());

        FakeTransport first = new FakeTransport();
        AtomicBoolean uiDeadlineElapsed = new AtomicBoolean();
        PushWorker.PairingServices delayedButAvailable = new PushWorker.PairingServices() {
            @Override public void token(PushSdk.TokenResult result) {
                new Thread(() -> {
                    try { Thread.sleep(25); }
                    catch (InterruptedException interrupted) { Thread.currentThread().interrupt(); return; }
                    uiDeadlineElapsed.set(true);
                    try { store.update(state -> state.put("token", "native-token-1234567890ABCDEF")); }
                    catch (IOException error) { throw new AssertionError(error); }
                    result.ready("native-token-1234567890ABCDEF");
                }).start();
            }
            @Override public void exchange(String id, long revocation, java.util.function.BooleanSupplier active) throws Exception {
                PushPairing.exchange(context, id, first, revocation, active);
            }
        };
        ListenableWorker.Result ambiguous = PushWorker.runPairing(context, PAIRING, () -> false,
                delayedButAvailable, 9000);
        assertEquals(ListenableWorker.Result.retry().getClass(), ambiguous.getClass());
        assertTrue("the background worker remains active after the UI deadline", uiDeadlineElapsed.get());
        assertEquals("the first worker attempt makes exactly one exchange POST", 1, first.calls.size());
        assertTrue(first.calls.get(0).startsWith("POST "));
        assertTrue(store.read().optBoolean("everExchangeAttempted"));
        assertTrue(store.read().getJSONObject("pair").optBoolean("exchangeAttempted"));

        FakeTransport retry = new FakeTransport();
        PushWorker.PairingServices reconcileOnly = new PushWorker.PairingServices() {
            @Override public void token(PushSdk.TokenResult result) { fail("consumed proof must not request a new token"); }
            @Override public void exchange(String id, long revocation, java.util.function.BooleanSupplier active) throws Exception {
                PushPairing.exchange(context, id, retry, revocation, active);
            }
        };
        ListenableWorker.Result exhausted = PushWorker.runPairing(context, PAIRING, () -> false, reconcileOnly, 1000);
        assertEquals(ListenableWorker.Result.failure().getClass(), exhausted.getClass());
        assertEquals("restart reconciliation uses GET and cannot repeat the consumed POST", 1, retry.calls.size());
        assertTrue(retry.calls.get(0).startsWith("GET "));
    }

    @Test
    public void expiredAmbiguousExchangeRemainsGetOnlyAcrossDurableWorkerRetries() throws Exception {
        seedPair(System.currentTimeMillis() - 1000, true, CODE);
        store.update(state -> state.put("everExchangeAttempted", true));
        grantNotificationPermission();

        for (int attempt = 1; attempt <= 3; attempt++) {
            FakeTransport reconcile = new FakeTransport();
            PushWorker.PairingServices services = new PushWorker.PairingServices() {
                @Override public void token(PushSdk.TokenResult result) { fail("an attempted exchange needs no token retry"); }
                @Override public void exchange(String id, long revocation, java.util.function.BooleanSupplier active) throws Exception {
                    PushPairing.exchange(context, id, reconcile, revocation, active);
                }
            };
            ListenableWorker.Result result = PushWorker.runPairing(context, PAIRING, () -> false, services, 1000);
            assertEquals("expired proof is reconciled with GET on every run", 1, reconcile.calls.size());
            assertTrue(reconcile.calls.get(0).startsWith("GET "));
            assertEquals(attempt < 3 ? ListenableWorker.Result.retry().getClass()
                    : ListenableWorker.Result.failure().getClass(), result.getClass());
            assertEquals(attempt, store.read().getJSONObject("pair").getInt("workerAttempts"));
        }

        JSONObject state = store.read();
        assertEquals("consumed proof remains available for server reconciliation", CODE,
                state.getJSONObject("pair").getString("completionCode"));
        assertTrue(state.optBoolean("everExchangeAttempted"));
        assertEquals("connection_failed", state.optString("lastConnectionStatus"));
    }

    @Test
    public void persistedOptOutWithAmbiguousProofPreventsAnyAutomaticRequest() throws Exception {
        seedPair(System.currentTimeMillis() - 1000, true, CODE);
        store.update(state -> state.put("optedOut", true).put("everExchangeAttempted", true));
        FakeTransport api = new FakeTransport();

        assertEquals(RETURN_URL, PushPairing.bootstrap(context, RETURN_URL, api));
        assertTrue("sticky opt-out must short-circuit before any unsafe automatic recovery", api.calls.isEmpty());
        PushPairing.exchange(context, PAIRING, api);
        assertTrue("opt-out also fences an already queued exchange", api.calls.isEmpty());
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
