package ru.heartbeatchecker.app;

import android.content.Context;
import android.content.ContextWrapper;
import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.os.Build;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.work.Constraints;
import androidx.work.ExistingWorkPolicy;
import androidx.work.NetworkType;
import androidx.work.OneTimeWorkRequest;
import androidx.work.WorkInfo;
import androidx.work.WorkManager;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.File;
import java.io.IOException;
import java.time.Instant;
import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicBoolean;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class PushStoreTest {
    private static final String INSTALLATION = "123e4567-e89b-12d3-a456-426614174000";
    private static final String MONITOR = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    private static final String INCIDENT = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
    private static final String OWNER = "11111111-1111-4111-8111-111111111111";
    private static final String SESSION = "22222222-2222-4222-8222-222222222222";
    private static final String CREDENTIAL = INSTALLATION + ".abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
    private Context applicationContext;
    private Context context;
    private File isolatedNoBackupDir;
    private PushStore store;
    private AuthStore authStore;
    private boolean previousLocallyBlocked;

    @Before
    public void setUp() throws Exception {
        previousLocallyBlocked = PushWork.locallyBlocked;
        applicationContext = ApplicationProvider.getApplicationContext();
        isolatedNoBackupDir = new File(applicationContext.getNoBackupFilesDir(),
                "push-store-test-" + java.util.UUID.randomUUID());
        if (!isolatedNoBackupDir.mkdirs() && !isolatedNoBackupDir.isDirectory())
            throw new IOException("Could not create isolated push store directory");
        assertEquals(applicationContext.getNoBackupFilesDir(), isolatedNoBackupDir.getParentFile());
        context = new ContextWrapper(applicationContext) {
            @Override public File getNoBackupFilesDir() { return isolatedNoBackupDir; }
        };
        assertSame(WorkManager.getInstance(applicationContext), WorkManager.getInstance(context));
        authStore = new AuthStore(context);
        clearAuthStore();
        authStore.saveSession(authSession(OWNER, SESSION), authStore.epoch());
        PushWork.locallyBlocked = false;
        store = new PushStore(context);
        store.update(state -> {
            java.util.Iterator<String> keys = state.keys();
            java.util.List<String> remove = new java.util.ArrayList<>();
            while (keys.hasNext()) remove.add(keys.next());
            for (String key : remove) state.remove(key);
        });
        install(1, 1);
    }

    @org.junit.After
    public void restoreDeliveryState() {
        PushWork.locallyBlocked = previousLocallyBlocked;
        try { clearAuthStore(); } catch (Exception ignored) { }
        deleteDirectory(isolatedNoBackupDir);
    }

    private static void deleteDirectory(File directory) {
        if (directory == null || !directory.exists()) return;
        File[] children = directory.listFiles();
        if (children != null) for (File child : children) deleteDirectory(child);
        directory.delete();
    }

    @Test
    public void encryptedStatePersistsInNoBackupStorageAndDeduplicatesDeliveries() throws Exception {
        String id = deliveryId(1);
        assertTrue(store.reserve(envelope(id, 1, System.currentTimeMillis() + 300_000)));
        assertFalse(store.reserve(envelope(id, 1, System.currentTimeMillis() + 300_000)));

        File file = new File(context.getNoBackupFilesDir(), "push-state.enc");
        assertTrue(file.isFile());
        assertTrue(file.length() > 30);
        byte[] bytes = java.nio.file.Files.readAllBytes(file.toPath());
        assertEquals("AES-GCM state uses the versioned encrypted format", 1, bytes[0]);
        assertFalse("delivery identifiers are not stored as plaintext",
                new String(bytes, java.nio.charset.StandardCharsets.UTF_8).contains(id));

        JSONObject persisted = new PushStore(context).read();
        assertEquals(id, persisted.getJSONObject("pending").getJSONObject(id).getString("deliveryId"));
        assertTrue("push state must live outside auto-backup", file.getParentFile().equals(context.getNoBackupFilesDir()));
    }

    @Test
    public void legacyInstallationWithoutNativeSessionOwnerStaysBehindMigrationGate() throws Exception {
        String legacyToken = "legacy-provider-token-for-upgrade-test";
        store.update(state -> {
            state.remove("accountId");
            state.remove("sessionId");
            state.put("token", legacyToken).put("registeredToken", legacyToken);
        });
        clearAuthStore(); // Simulate a TWA installation upgraded before native login.
        PushWork.locallyBlocked = false;
        ensureNotificationPermission();

        String id = deliveryId(80);
        PushWork.delivery(context, envelope(id, 1, System.currentTimeMillis() + 300_000));

        JSONObject state = store.read();
        assertTrue("existing encrypted installation credentials survive upgrade", state.has("credential"));
        assertTrue("server installation state survives for user-driven reconnect", state.has("installation"));
        assertEquals("provider token survives the app update in the old encrypted store", legacyToken, state.getString("token"));
        byte[] ciphertext = java.nio.file.Files.readAllBytes(
                new File(context.getNoBackupFilesDir(), "push-state.enc").toPath());
        assertFalse("provider token remains encrypted after the update", new String(ciphertext,
                java.nio.charset.StandardCharsets.UTF_8).contains(legacyToken));
        assertFalse("legacy TWA ownership does not authorize background delivery", state.optJSONObject("pending") != null
                && state.getJSONObject("pending").has(id));
        assertNull("upgrade does not infer an Android bearer session", authStore.active());
        assertFalse("old push data without exact native ownership remains gated", authStore.ownsPush(state));

        String queued = deliveryId(81);
        assertTrue(store.reserve(envelope(queued, 1, System.currentTimeMillis() + 300_000)));
        AtomicInteger fetches = new AtomicInteger();
        AtomicInteger displays = new AtomicInteger();
        androidx.work.ListenableWorker.Result result = runDelivery(queued, () -> false, fakeDelivery(fetches, displays));
        assertResult(androidx.work.ListenableWorker.Result.success(), result);
        assertEquals("legacy pending delivery is blocked before network access", 0, fetches.get());
        assertEquals("legacy pending delivery never displays without native session ownership", 0, displays.get());
        assertFalse("rejected legacy delivery is dropped", store.read().getJSONObject("pending").has(queued));
    }

    @Test
    public void expiredServerLeaseStopsBackgroundFetchEvenForCurrentOwner() throws Exception {
        ensureNotificationPermission();
        String id = deliveryId(82);
        JSONObject staleLeaseEnvelope = envelope(id, 1, System.currentTimeMillis() + 300_000);
        store.update(state -> {
            state.getJSONObject("installation").put("leaseExpiresAt", Instant.ofEpochMilli(System.currentTimeMillis() - 1000).toString());
            state.put("pending", new JSONObject().put(id, staleLeaseEnvelope));
        });
        AtomicInteger fetches = new AtomicInteger();
        AtomicInteger displays = new AtomicInteger();

        androidx.work.ListenableWorker.Result result = runDelivery(id, () -> false, fakeDelivery(fetches, displays));

        assertResult(androidx.work.ListenableWorker.Result.success(), result);
        assertEquals("expired lease is rejected before request", 0, fetches.get());
        assertEquals("expired lease cannot display an incident", 0, displays.get());
        assertFalse("expired lease clears the stale pending item", store.read().getJSONObject("pending").has(id));
    }

    @Test
    public void diagnosticsAreEncryptedBoundedAndRetainSpecificProviderFailure() throws Exception {
        String sdkMessage = "credential=secret-provider-token";
        List<ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException> failures = Arrays.asList(
                new ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException.HostAppNotInstalledException(sdkMessage));
        store.providerIssue(HeartbeatMessagingService.providerIssue(failures));
        store.providerIssue(PushStore.ProviderIssue.SDK_UNAVAILABLE);
        store.providerIssue(PushStore.ProviderIssue.OTHER);
        store.deliveryStage(PushStore.DeliveryStage.RECEIVED);
        store.deliveryStage(PushStore.DeliveryStage.QUEUED);

        JSONObject state = store.read();
        assertEquals(PushStore.ProviderIssue.DISTRIBUTOR_MISSING.name(),
                state.getJSONObject("providerDiagnostic").getString("code"));
        assertEquals(PushStore.DeliveryStage.QUEUED.name(),
                state.getJSONObject("deliveryDiagnostic").getString("code"));
        assertTrue("receive timestamp is retained independently of the latest stage", state.getLong("lastReceivedAt") > 0);
        assertFalse("provider diagnostics do not retain SDK messages", state.toString().contains(sdkMessage));
        assertFalse("delivery diagnostics do not retain identifiers or payloads", state.has("payload"));

        byte[] encrypted = java.nio.file.Files.readAllBytes(
                new File(context.getNoBackupFilesDir(), "push-state.enc").toPath());
        String ciphertext = new String(encrypted, java.nio.charset.StandardCharsets.UTF_8);
        assertFalse(ciphertext.contains("DISTRIBUTOR_MISSING"));
        assertFalse(ciphertext.contains(PushStore.DeliveryStage.QUEUED.name()));
        assertFalse(ciphertext.contains(sdkMessage));
        assertTrue("diagnostics keep encrypted state within its configured bound", encrypted.length < 8192);
    }

    @Test
    public void providerIssueClassifierUsesExceptionTypesAndStablePriority() {
        String secret = "must-not-be-persisted-provider-token";
        assertEquals(PushStore.ProviderIssue.DISTRIBUTOR_AUTH_REQUIRED,
                HeartbeatMessagingService.providerIssue(Arrays.asList(
                        new ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException.HostAppNotInstalledException(secret),
                        new ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException.UnauthorizedException(secret))));
        assertEquals(PushStore.ProviderIssue.DISTRIBUTOR_MISSING,
                HeartbeatMessagingService.providerIssue(Arrays.asList(
                        new ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException.HostAppBackgroundWorkPermissionNotGranted(secret),
                        new ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException.HostAppNotInstalledException(secret))));
        assertEquals(PushStore.ProviderIssue.DISTRIBUTOR_BACKGROUND_RESTRICTED,
                HeartbeatMessagingService.providerIssue(Arrays.asList(
                        new ru.rustore.sdk.pushclient.messaging.exception.RuStorePushClientException.HostAppBackgroundWorkPermissionNotGranted(secret))));
        assertEquals(PushStore.ProviderIssue.OTHER, HeartbeatMessagingService.providerIssue(null));
        assertEquals(PushStore.ProviderIssue.OTHER, HeartbeatMessagingService.providerIssue(java.util.Collections.emptyList()));
    }

    @Test
    public void pendingQueueIsBoundedAtSixteenEntries() throws Exception {
        for (int i = 1; i <= 16; i++) {
            assertTrue("reservation " + i, store.reserve(envelope(deliveryId(i), 1, System.currentTimeMillis() + 300_000)));
        }
        JSONObject state = store.read();
        assertEquals(16, state.getJSONObject("pending").length());
        assertFalse(store.reserve(envelope(deliveryId(17), 1, System.currentTimeMillis() + 300_000)));
        assertEquals(16, store.read().getJSONObject("pending").length());
    }

    @Test
    public void generationChangeResetsPendingSeenAndSequenceState() throws Exception {
        String id = deliveryId(1);
        assertTrue(store.reserve(envelope(id, 1, System.currentTimeMillis() + 300_000)));
        store.update(state -> state.put("seen", new JSONArray().put(deliveryId(2)))
                .put("sequences", new JSONObject().put(MONITOR, 12)));

        install(2, 2);
        JSONObject state = store.read();
        assertFalse(state.has("pending"));
        assertFalse(state.has("seen"));
        assertFalse(state.has("sequences"));
        assertEquals(2, state.getJSONObject("installation").getLong("bindingGeneration"));
    }

    @Test
    public void ambiguousNativeBindRetainsPendingAndRetriesExactBodyBeforeResumingDelivery() throws Exception {
        ensureNotificationPermission();
        String id = deliveryId(83);
        assertTrue(store.reserve(envelope(id, 1, System.currentTimeMillis() + 300_000)));
        JSONObject bindBody = new JSONObject().put("installationCredential", CREDENTIAL)
                .put("provider", "rustore").put("token", "push-token-from-native-sdk").put("permission", "granted")
                .put("bindingGeneration", 1).put("registrationVersion", 1);
        store.update(state -> state.put("pendingBind", new JSONObject().put("accountId", OWNER)
                .put("sessionId", SESSION).put("body", bindBody)));
        JSONObject durable = store.read().getJSONObject("pendingBind");
        assertEquals(bindBody.toString(), durable.getJSONObject("body").toString());

        AtomicInteger fetches = new AtomicInteger();
        AtomicInteger displays = new AtomicInteger();
        PushSdk.bindingInProgress = true;
        PushWork.locallyBlocked = true;
        try {
            assertResult(androidx.work.ListenableWorker.Result.success(),
                    runDelivery(id, () -> false, fakeDelivery(fetches, displays)));
            assertEquals("delivery waits locally while bind result is unknown", 0, fetches.get());
            assertTrue("bind uncertainty does not discard the queued delivery", store.read().getJSONObject("pending").has(id));

            // A retry for the same owner and generation resolves the original request;
            // accepting it removes the retry record without dropping queued work.
            store.acceptInstallation(installation(1, 1), CREDENTIAL, OWNER, SESSION);
            JSONObject resolved = store.read();
            assertFalse("successful owner-bound bind clears its exact pending request", resolved.has("pendingBind"));
            assertTrue("same generation preserves pending delivery", resolved.getJSONObject("pending").has(id));
            assertEquals(1, resolved.getJSONObject("installation").getLong("bindingGeneration"));
        } finally {
            PushSdk.bindingInProgress = false;
            PushWork.locallyBlocked = false;
        }
        assertResult(androidx.work.ListenableWorker.Result.success(),
                runDelivery(id, () -> false, fakeDelivery(fetches, displays)));
        assertEquals("delivery resumes after exact bind resolves", 1, fetches.get());
        assertEquals("only verified delivery is displayed", 1, displays.get());
        assertFalse(store.read().getJSONObject("pending").has(id));
    }

    @Test
    public void priorDownSequenceCannotDisplayAfterNewerIncidentWasCommitted() throws Exception {
        long expiry = System.currentTimeMillis() + 300_000;
        String newer = deliveryId(10);
        String stale = deliveryId(9);
        assertTrue(store.reserve(envelope(newer, 1, expiry)));
        AtomicInteger displayed = new AtomicInteger();
        assertTrue(store.commitDelivery(newer, delivery(newer, 20, "up", expiry), displayed::incrementAndGet));
        assertEquals(1, displayed.get());

        assertTrue(store.reserve(envelope(stale, 1, expiry)));
        assertFalse(store.commitDelivery(stale, delivery(stale, 19, "down", expiry), displayed::incrementAndGet));
        assertEquals("late DOWN is suppressed by the monitor sequence watermark", 1, displayed.get());
        JSONObject state = store.read();
        assertEquals(20, state.getJSONObject("sequences").getLong(MONITOR));
        assertFalse(state.getJSONObject("pending").has(stale));
    }

    @Test
    public void sequenceWatermarkStillSuppressesDeliveryAfterItsSeenIdWasEvicted() throws Exception {
        String stale = deliveryId(1000);
        JSONArray seen = new JSONArray().put(stale);
        for (int i = 1; i <= 255; i++) seen.put(deliveryId(i));
        store.update(state -> state.put("seen", seen).put("sequences", new JSONObject().put(MONITOR, 20)));

        store.drop(deliveryId(3000));
        JSONArray afterEviction = store.read().getJSONArray("seen");
        assertEquals(256, afterEviction.length());
        assertFalse(contains(afterEviction, stale));

        assertTrue("the evicted delivery id can be reserved again", store.reserve(
                envelope(stale, 1, System.currentTimeMillis() + 300_000)));
        AtomicInteger displayed = new AtomicInteger();
        assertFalse(store.commitDelivery(stale,
                delivery(stale, 19, "down", System.currentTimeMillis() + 300_000), displayed::incrementAndGet));
        assertEquals(0, displayed.get());
        assertEquals(20, store.read().getJSONObject("sequences").getLong(MONITOR));
    }

    @Test
    public void staleInstallationGenerationOrRegistrationVersionCannotReplaceCurrentState() throws Exception {
        install(2, 3);
        JSONObject before = store.read().getJSONObject("installation");
        assertStaleInstallationRejected(installation(1, 4));
        assertStaleInstallationRejected(installation(2, 2));
        JSONObject after = store.read().getJSONObject("installation");
        assertEquals(2, after.getLong("bindingGeneration"));
        assertEquals(3, after.getLong("registrationVersion"));
        assertEquals(before.getLong("bindingGeneration"), after.getLong("bindingGeneration"));
        assertEquals(before.getLong("registrationVersion"), after.getLong("registrationVersion"));
    }

    @Test
    public void lateResponseFromRetiredCredentialCannotReplaceOrClearCurrentInstallation() throws Exception {
        String currentCredential = INSTALLATION + ".abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
        String retiredCredential = INSTALLATION + ".ZYXWVUTSRQPONMLKJIHGFEDCBA9876543210abcdefG";
        store.update(state -> state.put("enabled", true).put("credential", currentCredential)
                .put("token", "current-token")
                .put("pending", new JSONObject().put(deliveryId(1), envelope(deliveryId(1), 1,
                        System.currentTimeMillis() + 300_000)))
                .put("seen", new JSONArray().put(deliveryId(2)))
                .put("sequences", new JSONObject().put(MONITOR, 12)));
        store.acceptInstallation(installation(2, 3), currentCredential);
        JSONObject before = store.read();
        WorkManager work = WorkManager.getInstance(context);
        String workName = PushWork.DELIVERY + ":stale-credential-sentinel";
        work.cancelUniqueWork(workName).getResult().get();
        OneTimeWorkRequest sentinel = new OneTimeWorkRequest.Builder(PushWorker.class)
                .setConstraints(new Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED)
                        .setRequiresDeviceIdle(true).build())
                .addTag(PushWork.DELIVERY).build();
        work.enqueueUniqueWork(workName, ExistingWorkPolicy.REPLACE, sentinel).getResult().get();
        NotificationManager notifications = context.getSystemService(NotificationManager.class);
        notifications.createNotificationChannel(new NotificationChannel(PushNotifications.CHANNEL,
                "Incidents", NotificationManager.IMPORTANCE_DEFAULT));
        if (Build.VERSION.SDK_INT >= 33
                && context.checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)
                != android.content.pm.PackageManager.PERMISSION_GRANTED) {
            InstrumentationRegistry.getInstrumentation().getUiAutomation()
                    .grantRuntimePermission(context.getPackageName(), android.Manifest.permission.POST_NOTIFICATIONS);
        }
        notifications.notify("push:stale-credential-sentinel", 0,
                new Notification.Builder(context, PushNotifications.CHANNEL)
                        .setSmallIcon(R.drawable.ic_notification).setContentTitle("Sentinel").build());
        try {
            PushWork.accept(context, installation(3, 4), retiredCredential);

            assertEquals("a retired credential response must leave all current local state intact",
                    before.toString(), store.read().toString());
            assertTrue("a stale response must not clear the current push notification",
                    hasNotification(notifications, "push:stale-credential-sentinel"));
            assertEquals("a stale response must not cancel current delivery work", WorkInfo.State.ENQUEUED,
                    work.getWorkInfosForUniqueWork(workName).get().get(0).getState());
        } finally {
            work.cancelUniqueWork(workName).getResult().get();
            notifications.cancel("push:stale-credential-sentinel", 0);
        }
    }

    @Test
    public void exhaustedAutomaticRevocationRemainsQueuedForExplicitReconnectRetry() throws Exception {
        String retiredCredential = INSTALLATION + ".abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
        store.update(state -> state.put("enabled", false).put("retired", new JSONArray()
                .put(new JSONObject().put("credential", retiredCredential).put("attempts", 3))));

        assertFalse("automatic retries stop, but unresolved revocation still blocks a new pairing",
                PushWork.revokePending(context, false));
        JSONArray retired = store.read().getJSONArray("retired");
        assertEquals(1, retired.length());
        assertEquals(retiredCredential, retired.getJSONObject(0).getString("credential"));
        assertEquals(3, retired.getJSONObject(0).getInt("attempts"));
    }

    @Test
    public void stoppedBeforeFetchKeepsPendingWithoutSpendingAttempt() throws Exception {
        String id = reserveDelivery(1);
        AtomicInteger fetches = new AtomicInteger();

        androidx.work.ListenableWorker.Result result = runDelivery(id, () -> true, fakeDelivery(fetches, new AtomicInteger()));

        assertResult(androidx.work.ListenableWorker.Result.success(), result);
        assertEquals(0, fetches.get());
        JSONObject pending = store.read().getJSONObject("pending").getJSONObject(id);
        assertEquals(0, pending.optInt("fetchAttempts"));
        assertFalse(contains(store.read().optJSONArray("seen"), id));
    }

    @Test
    public void stopDuringFetchIOExceptionKeepsPendingAndNextRunDisplaysOnce() throws Exception {
        String id = reserveDelivery(2);
        AtomicInteger fetches = new AtomicInteger();
        AtomicInteger displays = new AtomicInteger();
        AtomicBoolean stopped = new AtomicBoolean();

        androidx.work.ListenableWorker.Result first = runDelivery(id, stopped::get, new PushWorker.DeliveryServices() {
            @Override public JSONObject fetch(String deliveryId, String credential) throws IOException {
                fetches.incrementAndGet();
                stopped.set(true);
                throw new IOException("simulated constraint stop");
            }
            @Override public void display(JSONObject content) { displays.incrementAndGet(); }
        });

        assertResult(androidx.work.ListenableWorker.Result.success(), first);
        assertTrue(store.read().getJSONObject("pending").has(id));
        assertEquals(1, store.read().getJSONObject("pending").getJSONObject(id).getInt("fetchAttempts"));
        assertFalse(contains(store.read().optJSONArray("seen"), id));

        androidx.work.ListenableWorker.Result second = runDelivery(id, () -> false, fakeDelivery(fetches, displays));
        assertResult(androidx.work.ListenableWorker.Result.success(), second);
        assertEquals(2, fetches.get());
        assertEquals(1, displays.get());
        assertFalse(store.read().optJSONObject("pending").has(id));
        assertTrue(contains(store.read().optJSONArray("seen"), id));

        runDelivery(id, () -> false, fakeDelivery(fetches, displays));
        assertEquals("committed delivery is not fetched or displayed again", 2, fetches.get());
        assertEquals(1, displays.get());
    }

    @Test
    public void stopAfterValidResponseRetainsPendingUntilLaterRun() throws Exception {
        String id = reserveDelivery(3);
        AtomicInteger fetches = new AtomicInteger();
        AtomicInteger displays = new AtomicInteger();
        AtomicBoolean stopped = new AtomicBoolean();
        PushWorker.DeliveryServices services = new PushWorker.DeliveryServices() {
            @Override public JSONObject fetch(String deliveryId, String credential) throws org.json.JSONException {
                fetches.incrementAndGet();
                stopped.set(true);
                return delivery(deliveryId, 3, "down", System.currentTimeMillis() + 300_000);
            }
            @Override public void display(JSONObject content) { displays.incrementAndGet(); }
        };

        androidx.work.ListenableWorker.Result stoppedResult = runDelivery(id, stopped::get, services);

        assertResult(androidx.work.ListenableWorker.Result.success(), stoppedResult);
        assertEquals(1, fetches.get());
        assertEquals(0, displays.get());
        assertTrue(store.read().getJSONObject("pending").has(id));
        assertFalse(contains(store.read().optJSONArray("seen"), id));

        androidx.work.ListenableWorker.Result retry = runDelivery(id, () -> false, fakeDelivery(fetches, displays));
        assertResult(androidx.work.ListenableWorker.Result.success(), retry);
        assertEquals(2, fetches.get());
        assertEquals(1, displays.get());
        assertFalse(store.read().optJSONObject("pending").has(id));
        assertTrue(contains(store.read().optJSONArray("seen"), id));
    }

    @Test
    public void threeActualTransientFetchFailuresExhaustDeliveryBudget() throws Exception {
        String id = reserveDelivery(4);
        AtomicInteger fetches = new AtomicInteger();
        AtomicInteger displays = new AtomicInteger();
        PushWorker.DeliveryServices transientFailure = new PushWorker.DeliveryServices() {
            @Override public JSONObject fetch(String deliveryId, String credential) throws IOException {
                fetches.incrementAndGet();
                throw new IOException("simulated transient network failure");
            }
            @Override public void display(JSONObject content) { displays.incrementAndGet(); }
        };

        assertResult(androidx.work.ListenableWorker.Result.retry(), runDelivery(id, () -> false, transientFailure));
        assertResult(androidx.work.ListenableWorker.Result.retry(), runDelivery(id, () -> false, transientFailure));
        assertResult(androidx.work.ListenableWorker.Result.success(), runDelivery(id, () -> false, transientFailure));

        assertEquals(3, fetches.get());
        assertEquals(0, displays.get());
        assertFalse(store.read().optJSONObject("pending").has(id));
        assertTrue(contains(store.read().optJSONArray("seen"), id));
    }

    @Test
    public void stoppedServerFailureKeepsPendingForLaterRun() throws Exception {
        String id = reserveDelivery(10);
        AtomicInteger fetches = new AtomicInteger();
        AtomicBoolean stopped = new AtomicBoolean();
        PushWorker.DeliveryServices serverFailure = new PushWorker.DeliveryServices() {
            @Override public JSONObject fetch(String deliveryId, String credential) throws IOException {
                fetches.incrementAndGet();
                stopped.set(true);
                throw new PushApi.Failure(503);
            }
            @Override public void display(JSONObject content) { fail("a transient failure cannot display"); }
        };

        assertResult(androidx.work.ListenableWorker.Result.success(), runDelivery(id, stopped::get, serverFailure));

        assertEquals(1, fetches.get());
        assertEquals(1, store.read().getJSONObject("pending").getJSONObject(id).getInt("fetchAttempts"));
        assertFalse(contains(store.read().optJSONArray("seen"), id));
    }

    @Test
    public void inflightResponseAfterRebindCannotDisplayOrChangeNewBinding() throws Exception {
        String id = reserveDelivery(5);
        AtomicInteger displays = new AtomicInteger();
        PushWorker.DeliveryServices rebindDuringFetch = new PushWorker.DeliveryServices() {
            @Override public JSONObject fetch(String deliveryId, String credential) throws IOException, org.json.JSONException {
                try { install(2, 2); }
                catch (Exception error) { throw new IOException(error); }
                return delivery(deliveryId, 5, "down", System.currentTimeMillis() + 300_000);
            }
            @Override public void display(JSONObject content) { displays.incrementAndGet(); }
        };

        androidx.work.ListenableWorker.Result result = runDelivery(id, () -> false, rebindDuringFetch);

        JSONObject state = store.read();
        assertResult(androidx.work.ListenableWorker.Result.success(), result);
        assertEquals(0, displays.get());
        assertEquals(2, state.getJSONObject("installation").getLong("bindingGeneration"));
        assertFalse(state.optJSONObject("pending") != null && state.getJSONObject("pending").has(id));
        assertFalse(contains(state.optJSONArray("seen"), id));
        assertFalse(state.has("sequences"));
    }

    @Test
    public void inflightResponseAfterLogoutCannotDisplayOrRecreatePending() throws Exception {
        String id = reserveDelivery(9);
        AtomicInteger displays = new AtomicInteger();
        PushWorker.DeliveryServices logoutDuringFetch = new PushWorker.DeliveryServices() {
            @Override public JSONObject fetch(String deliveryId, String credential) throws IOException, org.json.JSONException {
                store.update(state -> {
                    state.put("enabled", false);
                    for (String key : new String[]{"credential", "installation", "pending", "seen", "sequences"})
                        state.remove(key);
                });
                return delivery(deliveryId, 9, "down", System.currentTimeMillis() + 300_000);
            }
            @Override public void display(JSONObject content) { displays.incrementAndGet(); }
        };

        androidx.work.ListenableWorker.Result result = runDelivery(id, () -> false, logoutDuringFetch);

        JSONObject state = store.read();
        assertResult(androidx.work.ListenableWorker.Result.success(), result);
        assertEquals(0, displays.get());
        assertFalse(state.optBoolean("enabled"));
        assertFalse(state.has("credential"));
        assertFalse(state.has("installation"));
        assertFalse(state.optJSONObject("pending") != null && state.getJSONObject("pending").has(id));
        assertFalse(contains(state.optJSONArray("seen"), id));
        assertFalse(state.has("sequences"));
    }

    @Test
    public void deliveryWorkUsesApiSpecificExpeditedPolicyOnlyForDelivery() {
        androidx.work.OneTimeWorkRequest api30 = PushWork.work("delivery", deliveryId(6), PushWork.DELIVERY, 30);
        androidx.work.OneTimeWorkRequest api31 = PushWork.work("delivery", deliveryId(7), PushWork.DELIVERY, 31);
        androidx.work.OneTimeWorkRequest registration = PushWork.work("registration", "", PushWork.REGISTRATION, 31);
        androidx.work.OneTimeWorkRequest revoke = PushWork.work("revoke", "", "heartbeat-push-revoke", 31);

        assertFalse(api30.getWorkSpec().expedited);
        assertFalse("registration keeps ordinary scheduling", registration.getWorkSpec().expedited);
        assertFalse("revocation keeps ordinary scheduling", revoke.getWorkSpec().expedited);
        assertTrue(api31.getWorkSpec().expedited);
        assertEquals(androidx.work.OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST,
                api31.getWorkSpec().outOfQuotaPolicy);
        assertEquals(NetworkType.CONNECTED, api31.getWorkSpec().constraints.getRequiredNetworkType());
    }

    @Test
    public void oversizedEncryptedStateWritePreservesPreviousFileAndDoesNotDisplay() throws Exception {
        String id = deliveryId(1);
        // Keep the raw-file comparison atomic with respect to legitimate background store updates.
        synchronized (PushStore.LOCK) {
            assertTrue(store.reserve(envelope(id, 1, System.currentTimeMillis() + 300_000)));
            File file = new File(context.getNoBackupFilesDir(), "push-state.enc");
            byte[] before = java.nio.file.Files.readAllBytes(file.toPath());
            AtomicInteger displayed = new AtomicInteger();

            try {
                store.update(state -> state.put("oversized", repeat("x", 1024 * 1024)));
                fail("a state larger than the encrypted storage cap was accepted");
            } catch (IOException expected) { }

            assertArrayEquals("failed write must leave the last committed ciphertext intact", before,
                    java.nio.file.Files.readAllBytes(file.toPath()));
            JSONObject readable = store.read();
            assertTrue(readable.getJSONObject("pending").has(id));
            assertFalse(readable.has("oversized"));
            assertEquals("a failed storage write must not run a display callback", 0, displayed.get());
        }
    }

    private void install(long generation, long registrationVersion) throws Exception {
        store.update(state -> state.put("enabled", true).put("credential", CREDENTIAL)
                .put("accountId", OWNER).put("sessionId", SESSION));
        store.acceptInstallation(installation(generation, registrationVersion), CREDENTIAL);
    }

    private void clearAuthStore() throws Exception {
        JSONObject state = authStore.read();
        java.util.Iterator<String> keys = state.keys();
        java.util.List<String> remove = new java.util.ArrayList<>();
        while (keys.hasNext()) remove.add(keys.next());
        for (String key : remove) authStore.remove(key);
    }

    private static JSONObject authSession(String accountId, String sessionId) throws Exception {
        String token = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
        String revoke = "BCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghi";
        return new JSONObject().put("account", new JSONObject().put("id", accountId))
                .put("session", new JSONObject().put("id", sessionId).put("token", token)
                        .put("revokeToken", revoke).put("expiresAt", Instant.ofEpochMilli(System.currentTimeMillis() + 3_600_000).toString()));
    }

    private String reserveDelivery(int sequence) throws Exception {
        ensureNotificationPermission();
        String id = deliveryId(sequence);
        assertTrue("delivery must be accepted into the real pending store",
                store.reserve(envelope(id, 1, System.currentTimeMillis() + 300_000)));
        return id;
    }

    private androidx.work.ListenableWorker.Result runDelivery(String id, java.util.function.BooleanSupplier stopped,
                                                                PushWorker.DeliveryServices services) throws Exception {
        return PushWorker.runDelivery(context, id, stopped, services);
    }

    private static PushWorker.DeliveryServices fakeDelivery(AtomicInteger fetches, AtomicInteger displays) {
        return new PushWorker.DeliveryServices() {
            @Override public JSONObject fetch(String deliveryId, String credential) throws org.json.JSONException {
                fetches.incrementAndGet();
                return delivery(deliveryId, 10, "down", System.currentTimeMillis() + 300_000);
            }
            @Override public void display(JSONObject content) { displays.incrementAndGet(); }
        };
    }

    private void ensureNotificationPermission() {
        PushNotifications.channel(context);
        if (Build.VERSION.SDK_INT >= 33
                && context.checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS)
                != android.content.pm.PackageManager.PERMISSION_GRANTED) {
            InstrumentationRegistry.getInstrumentation().getUiAutomation()
                    .grantRuntimePermission(context.getPackageName(), android.Manifest.permission.POST_NOTIFICATIONS);
        }
    }

    private static void assertResult(androidx.work.ListenableWorker.Result expected,
                                     androidx.work.ListenableWorker.Result actual) {
        assertEquals(expected.getClass(), actual.getClass());
    }

    private static JSONObject installation(long generation, long registrationVersion) throws Exception {
        return new JSONObject().put("id", INSTALLATION).put("provider", "rustore")
                .put("deviceLabel", "Instrumentation device").put("status", "ready")
                .put("bindingGeneration", generation).put("registrationVersion", registrationVersion)
                .put("leaseExpiresAt", Instant.ofEpochMilli(System.currentTimeMillis() + 600_000).toString())
                .put("lastPairingId", JSONObject.NULL).put("lastSeenAt", Instant.now().toString());
    }

    private void assertStaleInstallationRejected(JSONObject value) throws Exception {
        try {
            store.acceptInstallation(value, CREDENTIAL);
            fail("stale installation response was accepted");
        } catch (IOException expected) { }
    }

    private static boolean contains(JSONArray values, String expected) {
        if (values == null) return false;
        for (int i = 0; i < values.length(); i++) if (expected.equals(values.optString(i))) return true;
        return false;
    }

    private static boolean hasNotification(NotificationManager manager, String tag) {
        for (android.service.notification.StatusBarNotification notification : manager.getActiveNotifications()) {
            if (tag.equals(notification.getTag())) return true;
        }
        return false;
    }

    private static String repeat(String value, int count) {
        StringBuilder result = new StringBuilder(value.length() * count);
        for (int i = 0; i < count; i++) result.append(value);
        return result.toString();
    }

    private static JSONObject envelope(String id, long generation, long expiry) {
        Map<String, String> value = new HashMap<>();
        value.put("v", "1"); value.put("deliveryId", id); value.put("installationId", INSTALLATION);
        value.put("bindingGeneration", Long.toString(generation));
        value.put("expiresAt", Instant.ofEpochMilli(expiry).toString());
        try { return PushContract.envelope(value, System.currentTimeMillis()); }
        catch (Exception error) { throw new AssertionError(error); }
    }

    private static String deliveryId(int sequence) {
        return String.format(java.util.Locale.ROOT, "00000000-0000-4000-8000-%012x", sequence);
    }

    private static JSONObject delivery(String id, long sequence, String kind, long expiry) throws org.json.JSONException {
        return new JSONObject().put("deliveryId", id).put("monitorId", MONITOR).put("incidentId", INCIDENT)
                .put("kind", kind).put("title", "Monitor " + kind).put("body", "Incident update")
                .put("sequence", sequence).put("occurredAt", Instant.now().toString())
                .put("expiresAt", Instant.ofEpochMilli(expiry).toString());
    }
}
