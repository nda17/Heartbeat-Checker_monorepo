package ru.heartbeatchecker.app;

import android.content.Context;
import android.net.Uri;

import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;

import com.vk.push.common.messaging.ClickActionType;

import org.json.JSONObject;
import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

import java.io.File;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.Iterator;
import java.util.List;
import java.util.Map;

import ru.rustore.sdk.pushclient.messaging.model.Notification;
import ru.rustore.sdk.pushclient.messaging.model.RemoteMessage;

import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public final class PushEnvelopeInstrumentationTest {
    private static final String DELIVERY = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    private static final String INSTALLATION = "123e4567-e89b-12d3-a456-426614174000";
    private static final String PRIVATE_MARKER = "private-payload-marker";
    private Context context;
    private PushStore store;

    @Before
    public void clearDiagnostics() throws Exception {
        context = ApplicationProvider.getApplicationContext();
        store = new PushStore(context);
        store.update(state -> {
            Iterator<String> keys = state.keys();
            List<String> remove = new ArrayList<>();
            while (keys.hasNext()) remove.add(keys.next());
            for (String key : remove) state.remove(key);
        });
    }

    @Test
    public void actualRuStoreMessageAcceptsOnlyNullOrSemanticallyEmptyNotification() throws Exception {
        long now = System.currentTimeMillis();
        Map<String, String> data = envelope(now + 60_000);

        assertAccepted(message(data, null), now);
        assertAccepted(message(data, notification(null, null, null, null, null, null, null, null)), now);
        assertAccepted(message(data, notification("", "", "", Uri.parse(""), "", "", "", ClickActionType.DEFAULT)), now);
    }

    @Test
    public void everyNonemptyNotificationPayloadFieldIsRejectedWithTypedReason() {
        long now = System.currentTimeMillis();
        Map<String, String> data = envelope(now + 60_000);
        Notification[] payloads = {
                notification("title-marker", null, null, null, null, null, null, null),
                notification(null, "body-marker", null, null, null, null, null, null),
                notification(null, null, "channel-marker", null, null, null, null, null),
                notification(null, null, null, Uri.parse("content://image-marker"), null, null, null, null),
                notification(null, null, null, null, "color-marker", null, null, null),
                notification(null, null, null, null, null, "icon-marker", null, null),
                notification(null, null, null, null, null, null, "action-marker", null),
                notification(" ", null, null, null, null, null, null, null),
                notification(null, null, null, null, null, null, null, ClickActionType.DEEP_LINK)
        };

        for (Notification payload : payloads) {
            assertReason(PushContract.EnvelopeReason.NOTIFICATION_CONTENT, message(data, payload), now);
        }
    }

    @Test
    public void exactFiveFieldEnvelopeAndTypedFailureReasonsArePreserved() throws Exception {
        long now = System.currentTimeMillis();
        Map<String, String> data = envelope(now + 60_000);
        JSONObject parsed = PushContract.envelope(message(data, null), now);
        assertEquals(5, parsed.length());
        assertEquals("1", parsed.getString("v"));
        assertEquals(DELIVERY, parsed.getString("deliveryId"));
        assertEquals(INSTALLATION, parsed.getString("installationId"));
        assertEquals("17", parsed.getString("bindingGeneration"));

        JSONObject testEnvelope = PushContract.envelope(message(with(data, "v", "2"), null), now);
        assertEquals("test pushes use the same strict five-field opaque envelope", 5, testEnvelope.length());
        assertEquals("2", testEnvelope.getString("v"));

        assertReason(PushContract.EnvelopeReason.NULL_MESSAGE, null, now);
        assertMapReason(PushContract.EnvelopeReason.DATA_KEYS, null, now);
        assertReason(PushContract.EnvelopeReason.DATA_KEYS, message(new HashMap<>(), null), now);
        assertReason(PushContract.EnvelopeReason.DATA_KEYS, message(without(data, "expiresAt"), null), now);
        assertReason(PushContract.EnvelopeReason.DATA_KEYS, message(with(data, "title", "private-marker"), null), now);
        assertReason(PushContract.EnvelopeReason.VERSION, message(with(data, "v", "3"), null), now);
        assertReason(PushContract.EnvelopeReason.DATA_KEYS,
                message(with(with(data, "v", "2"), "title", "private-marker"), null), now);
        assertReason(PushContract.EnvelopeReason.DELIVERY_ID, message(with(data, "deliveryId", "invalid-private-marker"), null), now);
        assertReason(PushContract.EnvelopeReason.INSTALLATION_ID, message(with(data, "installationId", "not-a-uuid"), null), now);
        assertReason(PushContract.EnvelopeReason.GENERATION, message(with(data, "bindingGeneration", "0"), null), now);
        assertReason(PushContract.EnvelopeReason.GENERATION, message(with(data, "bindingGeneration", "01"), null), now);
        assertReason(PushContract.EnvelopeReason.GENERATION,
                message(with(data, "bindingGeneration", "9223372036854775808"), null), now);
        assertReason(PushContract.EnvelopeReason.EXPIRY_FORMAT, message(with(data, "expiresAt", "invalid-private-time"), null), now);
        assertReason(PushContract.EnvelopeReason.EXPIRED,
                message(envelope(now), null), now);
    }

    @Test
    public void nonStringSdkDataValuesAreRejectedWithTheirFieldReason() {
        long now = System.currentTimeMillis();
        Map<String, String> data = envelope(now + 60_000);

        assertReason(PushContract.EnvelopeReason.VERSION,
                message(withRawValue(data, "v", 1), null), now);
        assertReason(PushContract.EnvelopeReason.DELIVERY_ID,
                message(withRawValue(data, "deliveryId", 123), null), now);
        assertReason(PushContract.EnvelopeReason.INSTALLATION_ID,
                message(withRawValue(data, "installationId", 123), null), now);
        assertReason(PushContract.EnvelopeReason.GENERATION,
                message(withRawValue(data, "bindingGeneration", 17), null), now);
        assertReason(PushContract.EnvelopeReason.EXPIRY_FORMAT,
                message(withRawValue(data, "expiresAt", now + 60_000), null), now);
    }

    @Test
    public void rejectedMessageDiagnosticsKeepOnlyEnumReasonAndNeverPersistPayload() throws Exception {
        long now = System.currentTimeMillis();
        Map<String, String> data = with(envelope(now + 60_000), "title", PRIVATE_MARKER);
        RemoteMessage message = new RemoteMessage(PRIVATE_MARKER, 0, 0, PRIVATE_MARKER, PRIVATE_MARKER,
                data, PRIVATE_MARKER.getBytes(StandardCharsets.UTF_8),
                notification(PRIVATE_MARKER, null, null, null, null, null, null, null));

        PushContract.EnvelopeException rejection = rejected(message, now);
        assertEquals(PushContract.EnvelopeReason.NOTIFICATION_CONTENT, rejection.reason);
        assertFalse(rejection.getMessage().contains(PRIVATE_MARKER));
        store.deliveryStage(PushStore.DeliveryStage.RECEIVED);
        store.deliveryStage(PushStore.DeliveryStage.INVALID_ENVELOPE, rejection.reason);

        JSONObject state = store.read();
        JSONObject diagnostic = state.getJSONObject("deliveryDiagnostic");
        assertEquals(PushStore.DeliveryStage.INVALID_ENVELOPE.name(), diagnostic.getString("code"));
        assertEquals(PushContract.EnvelopeReason.NOTIFICATION_CONTENT.name(), diagnostic.getString("reason"));
        assertTrue(state.getLong("lastReceivedAt") > 0);
        assertFalse(state.toString().contains(PRIVATE_MARKER));
        assertFalse(state.toString().contains(DELIVERY));

        byte[] encrypted = java.nio.file.Files.readAllBytes(new File(context.getNoBackupFilesDir(), "push-state.enc").toPath());
        String ciphertext = new String(encrypted, StandardCharsets.UTF_8);
        assertFalse(ciphertext.contains(PRIVATE_MARKER));
        assertFalse(ciphertext.contains(PushContract.EnvelopeReason.NOTIFICATION_CONTENT.name()));

        store.deliveryStage(PushStore.DeliveryStage.QUEUED);
        assertFalse("later delivery stages replace the parser reason", store.read()
                .getJSONObject("deliveryDiagnostic").has("reason"));
    }

    @Test
    public void expiredMessageUsesExpiredStageWithoutInvalidEnvelopeReason() throws Exception {
        long now = System.currentTimeMillis();
        PushContract.EnvelopeException rejection = rejected(message(envelope(now), null), now);
        assertEquals(PushContract.EnvelopeReason.EXPIRED, rejection.reason);

        store.deliveryStage(PushStore.DeliveryStage.EXPIRED);
        JSONObject diagnostic = store.read().getJSONObject("deliveryDiagnostic");
        assertEquals(PushStore.DeliveryStage.EXPIRED.name(), diagnostic.getString("code"));
        assertFalse(diagnostic.has("reason"));
    }

    private static void assertAccepted(RemoteMessage message, long now) throws Exception {
        assertEquals(5, PushContract.envelope(message, now).length());
    }

    private static void assertReason(PushContract.EnvelopeReason expected, RemoteMessage message, long now) {
        assertEquals(expected, rejected(message, now).reason);
    }

    private static void assertMapReason(PushContract.EnvelopeReason expected, Map<String, String> data, long now) {
        try {
            PushContract.envelope(data, now);
            fail("invalid envelope data was accepted");
        } catch (PushContract.EnvelopeException invalid) {
            assertEquals(expected, invalid.reason);
        }
    }

    private static PushContract.EnvelopeException rejected(RemoteMessage message, long now) {
        try {
            PushContract.envelope(message, now);
            fail("invalid RuStore message was accepted");
        } catch (PushContract.EnvelopeException invalid) {
            return invalid;
        }
        throw new AssertionError("invalid RuStore message was accepted");
    }

    private static RemoteMessage message(Map<String, String> data, Notification notification) {
        return new RemoteMessage("message-id", 0, 0, "sender", null, data, null, notification);
    }

    private static Notification notification(String title, String body, String channelId, Uri imageUrl,
                                             String color, String icon, String clickAction,
                                             ClickActionType clickActionType) {
        return new Notification(title, body, channelId, imageUrl, color, icon, clickAction, clickActionType);
    }

    private static Map<String, String> envelope(long expiry) {
        Map<String, String> value = new HashMap<>();
        value.put("v", "1");
        value.put("deliveryId", DELIVERY);
        value.put("installationId", INSTALLATION);
        value.put("bindingGeneration", "17");
        value.put("expiresAt", Instant.ofEpochMilli(expiry).toString());
        return value;
    }

    private static Map<String, String> with(Map<String, String> value, String key, String item) {
        Map<String, String> copy = new HashMap<>(value);
        copy.put(key, item);
        return copy;
    }

    private static Map<String, String> without(Map<String, String> value, String key) {
        Map<String, String> copy = new HashMap<>(value);
        copy.remove(key);
        return copy;
    }

    @SuppressWarnings({"rawtypes", "unchecked"})
    private static Map<String, String> withRawValue(Map<String, String> value, String key, Object item) {
        Map copy = new HashMap(value);
        copy.put(key, item);
        return copy;
    }
}
