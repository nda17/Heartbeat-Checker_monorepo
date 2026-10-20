package ru.heartbeatchecker.app;

import android.content.Context;
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
import java.util.HashMap;
import java.util.Map;
import java.util.concurrent.atomic.AtomicInteger;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class PushStoreTest {
    private static final String INSTALLATION = "123e4567-e89b-12d3-a456-426614174000";
    private static final String MONITOR = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    private static final String INCIDENT = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";
    private static final String CREDENTIAL = INSTALLATION + ".abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG";
    private Context context;
    private PushStore store;

    @Before
    public void setUp() throws Exception {
        context = ApplicationProvider.getApplicationContext();
        store = new PushStore(context);
        store.update(state -> {
            java.util.Iterator<String> keys = state.keys();
            java.util.List<String> remove = new java.util.ArrayList<>();
            while (keys.hasNext()) remove.add(keys.next());
            for (String key : remove) state.remove(key);
        });
        install(1, 1);
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
    public void oversizedEncryptedStateWritePreservesPreviousFileAndDoesNotDisplay() throws Exception {
        String id = deliveryId(1);
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

    private void install(long generation, long registrationVersion) throws Exception {
        store.update(state -> state.put("enabled", true).put("credential", CREDENTIAL));
        store.acceptInstallation(installation(generation, registrationVersion), CREDENTIAL);
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

    private static JSONObject delivery(String id, long sequence, String kind, long expiry) throws Exception {
        return new JSONObject().put("deliveryId", id).put("monitorId", MONITOR).put("incidentId", INCIDENT)
                .put("kind", kind).put("title", "Monitor " + kind).put("body", "Incident update")
                .put("sequence", sequence).put("occurredAt", Instant.now().toString())
                .put("expiresAt", Instant.ofEpochMilli(expiry).toString());
    }
}
