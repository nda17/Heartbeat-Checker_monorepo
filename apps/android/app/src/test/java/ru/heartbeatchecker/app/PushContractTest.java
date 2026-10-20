package ru.heartbeatchecker.app;

import org.json.JSONException;
import org.json.JSONObject;
import org.junit.Test;

import java.time.Instant;
import java.util.HashMap;
import java.util.Map;

import static org.junit.Assert.*;

public final class PushContractTest {
    private static final String INSTALLATION = "123e4567-e89b-12d3-a456-426614174000";
    private static final String PAIRING = "987e6543-e21b-12d3-a456-426614174000";
    private static final String DELIVERY = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
    private static final String MONITOR = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    private static final String INCIDENT = "cccccccc-cccc-4ccc-8ccc-cccccccccccc";

    @Test
    public void setupUrlsAcceptOnlyTheTwoExpectedNativeHandoffs() {
        assertEquals("", PushContract.setupPair("https://heartbeat-checker.ru/android/push/connect"));
        assertEquals("complete", PushContract.setupPair("https://heartbeat-checker.ru/android/push/complete"));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru/android/push/connect?pairingId=" + PAIRING));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru/android/push/complete?pairingId=" + PAIRING));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru/android/push/complete?pairingId=" + PAIRING + "&token=secret"));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru/android/push/complete?pairingId=" + PAIRING.toUpperCase()));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru/android/push/complete?pairingId=%" + PAIRING));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru.evil.test/android/push/connect"));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru@evil.test/android/push/connect"));
        assertNull(PushContract.setupPair("http://heartbeat-checker.ru/android/push/connect"));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru:443/android/push/connect"));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru/android/push/connect#fragment"));
        assertNull(PushContract.setupPair("https://heartbeat-checker.ru/android/push/connect/"));
        assertNull(PushContract.setupPair(null));
    }

    @Test
    public void confirmationIsBoundToTheCanonicalSettingsUrlWithoutSecrets() {
        assertTrue(PushContract.confirmation(PushContract.ORIGIN + "/app/settings?pushPair=" + PAIRING, PAIRING));
        assertFalse(PushContract.confirmation(PushContract.ORIGIN + "/app/settings?pushPair=" + PAIRING + "&token=secret", PAIRING));
        assertFalse(PushContract.confirmation("https://evil.test/app/settings?pushPair=" + PAIRING, PAIRING));
        assertFalse(PushContract.confirmation(PushContract.ORIGIN + "/app/settings?pushPair=" + PAIRING.toUpperCase(), PAIRING));
        assertFalse(PushContract.confirmation(PushContract.ORIGIN + "/app/settings?pushPair=" + PAIRING, "not-a-uuid"));
    }

    @Test
    public void installationRequiresExactRawDtoFieldsAndRecognizedStatuses() throws Exception {
        String[] statuses = {"pending", "ready", "permission_denied", "session_expired", "session_revoked",
                "invalid_token", "provider_unavailable"};
        for (String status : statuses) {
            JSONObject parsed = PushContract.installation(installation(status, 0, 1));
            assertEquals(status, parsed.getString("status"));
        }
        JSONObject parsed = PushContract.installation(installation("pending", 0, 1));
        assertTrue(parsed.isNull("leaseExpiresAt"));
        assertTrue(parsed.isNull("lastPairingId"));

        JSONObject extra = installation("ready", 1, 1).put("credential", "must-not-be-in-install-dto");
        assertInvalidInstallation(extra);
        assertInvalidInstallation(without(installation("pending", 0, 1), "lastSeenAt"));
        assertInvalidInstallation(installation("unknown", 0, 1));
        assertInvalidInstallation(installation("ready", -1, 1));
        assertInvalidInstallation(installation("ready", 1, 0));
        assertInvalidInstallation(installation("ready", 1, 1).put("id", INSTALLATION.toUpperCase()));
        assertInvalidInstallation(installation("ready", 1, 1).put("provider", "fcm"));
        assertInvalidInstallation(installation("ready", 1, 1).put("lastSeenAt", JSONObject.NULL));
        assertInvalidInstallation(installation("ready", 1, 1).put("lastPairingId", "not-a-uuid"));
        assertInvalidInstallation(installation("ready", 1, 1).put("leaseExpiresAt", "not-an-instant"));
    }

    @Test
    public void pushEnvelopeIsDataOnlyExactAndFresh() throws Exception {
        long now = System.currentTimeMillis();
        Map<String, String> data = envelope(now + 60_000);
        assertEquals("1", PushContract.envelope(data, now).getString("v"));
        assertEquals("17", PushContract.envelope(data, now).getString("bindingGeneration"));

        Map<String, String> extra = new HashMap<>(data);
        extra.put("title", "private notification content");
        assertInvalidEnvelope(extra, now);
        Map<String, String> missing = new HashMap<>(data);
        missing.remove("expiresAt");
        assertInvalidEnvelope(missing, now);
        Map<String, String> expired = envelope(now);
        assertInvalidEnvelope(expired, now);
        assertInvalidEnvelope(with(data, "v", "2"), now);
        assertInvalidEnvelope(with(data, "deliveryId", DELIVERY.toUpperCase()), now);
        assertInvalidEnvelope(with(data, "installationId", "not-a-uuid"), now);
        assertInvalidEnvelope(with(data, "bindingGeneration", "0"), now);
        assertInvalidEnvelope(with(data, "bindingGeneration", "01"), now);
        assertInvalidEnvelope(with(data, "bindingGeneration", "1.0"), now);
        assertInvalidEnvelope(with(data, "bindingGeneration", "9223372036854775808"), now);
    }

    @Test
    public void deliveryEnforcesBoundsSequenceAndCanonicalIncidentUrl() throws Exception {
        long now = System.currentTimeMillis();
        JSONObject delivery = delivery(DELIVERY, 1, now + 60_000);
        assertSame(delivery, PushContract.delivery(delivery, DELIVERY, now));
        assertEquals(PushContract.ORIGIN + "/app?monitor=" + MONITOR + "&incident=" + INCIDENT,
                PushContract.incidentUrl(delivery));

        assertInvalidDelivery(delivery(DELIVERY, 0, now + 60_000), now);
        assertInvalidDelivery(delivery(DELIVERY, 1, now), now);
        assertInvalidDelivery(delivery("not-a-uuid", 1, now + 60_000), now, DELIVERY);
        assertInvalidDelivery(delivery(DELIVERY, 1, now + 60_000).put("kind", "heartbeat"), now);
        assertInvalidDelivery(delivery(DELIVERY, 1, now + 60_000).put("title", repeat("x", 101)), now);
        assertInvalidDelivery(delivery(DELIVERY, 1, now + 60_000).put("body", repeat("я", 1025)), now);
        assertInvalidDelivery(delivery(DELIVERY, 1, now + 60_000).put("body", "bad\nbody"), now);
        assertInvalidDelivery(delivery(DELIVERY, 1, now + 60_000).put("occurredAt", "not-an-instant"), now);
    }

    private static JSONObject installation(String status, long generation, long registrationVersion) throws Exception {
        return new JSONObject().put("id", INSTALLATION).put("provider", "rustore").put("deviceLabel", "Test Android")
                .put("status", status).put("bindingGeneration", generation).put("registrationVersion", registrationVersion)
                .put("leaseExpiresAt", JSONObject.NULL).put("lastPairingId", JSONObject.NULL)
                .put("lastSeenAt", "2026-10-20T12:00:00Z");
    }

    private static Map<String, String> envelope(long expiry) {
        Map<String, String> value = new HashMap<>();
        value.put("v", "1"); value.put("deliveryId", DELIVERY); value.put("installationId", INSTALLATION);
        value.put("bindingGeneration", "17"); value.put("expiresAt", Instant.ofEpochMilli(expiry).toString());
        return value;
    }

    private static JSONObject delivery(String id, long sequence, long expiry) throws Exception {
        return new JSONObject().put("deliveryId", id).put("monitorId", MONITOR).put("incidentId", INCIDENT)
                .put("kind", "down").put("title", "Monitor is down").put("body", "Check the service")
                .put("sequence", sequence).put("occurredAt", "2026-10-20T12:00:00Z")
                .put("expiresAt", Instant.ofEpochMilli(expiry).toString());
    }

    private static JSONObject without(JSONObject value, String key) throws Exception {
        JSONObject copy = new JSONObject(value.toString());
        copy.remove(key);
        return copy;
    }

    private static Map<String, String> with(Map<String, String> value, String key, String item) {
        Map<String, String> copy = new HashMap<>(value);
        copy.put(key, item);
        return copy;
    }

    private static String repeat(String value, int count) {
        StringBuilder result = new StringBuilder(value.length() * count);
        for (int i = 0; i < count; i++) result.append(value);
        return result.toString();
    }

    private static void assertInvalidInstallation(JSONObject value) {
        try { PushContract.installation(value); fail("invalid installation was accepted: " + value); }
        catch (Exception expected) { }
    }

    private static void assertInvalidEnvelope(Map<String, String> value, long now) {
        try { PushContract.envelope(value, now); fail("invalid envelope was accepted: " + value); }
        catch (Exception expected) { }
    }

    private static void assertInvalidDelivery(JSONObject value, long now) {
        assertInvalidDelivery(value, now, DELIVERY);
    }

    private static void assertInvalidDelivery(JSONObject value, long now, String id) {
        try { PushContract.delivery(value, id, now); fail("invalid delivery was accepted: " + value); }
        catch (Exception expected) { }
    }
}
