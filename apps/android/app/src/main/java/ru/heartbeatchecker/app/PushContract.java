package ru.heartbeatchecker.app;

import org.json.JSONException;
import org.json.JSONObject;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.Arrays;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.Iterator;
import java.util.regex.Pattern;

import com.vk.push.common.messaging.ClickActionType;
import ru.rustore.sdk.pushclient.messaging.model.Notification;
import ru.rustore.sdk.pushclient.messaging.model.RemoteMessage;

final class PushContract {
    static final String ORIGIN = "https://heartbeat-checker.ru";
    static final int JSON_LIMIT = 8192;
    private static final Pattern UUID = Pattern.compile("[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}");
    private static final Set<String> STATUSES = new HashSet<>(Arrays.asList("pending", "ready", "permission_denied",
            "session_expired", "session_revoked", "invalid_token", "provider_unavailable"));
    enum EnvelopeReason { NULL_MESSAGE, NOTIFICATION_CONTENT, DATA_KEYS, VERSION, DELIVERY_ID,
        INSTALLATION_ID, GENERATION, EXPIRY_FORMAT, EXPIRED, SDK_MESSAGE }
    static final class EnvelopeException extends JSONException {
        final EnvelopeReason reason;
        EnvelopeException(EnvelopeReason reason) { super(reason.name()); this.reason = reason; }
    }

    private PushContract() {}

    static boolean uuid(String value) { return value != null && UUID.matcher(value).matches(); }

    static boolean bounded(String value, int bytes) {
        return value != null && value.getBytes(StandardCharsets.UTF_8).length <= bytes
                && value.codePoints().noneMatch(c -> c < 32 || c == 127);
    }

    static boolean provider(String value) { return "rustore".equals(value) || "fcm".equals(value) || "huawei".equals(value); }

    static boolean token(String value) { return value != null && !value.isEmpty() && bounded(value, 4096); }

    static boolean secret(String value) { return value != null && value.matches("[A-Za-z0-9_-]{43}"); }

    static boolean credential(String value, String id) {
        return uuid(id) && value != null && value.matches(Pattern.quote(id) + "\\.[A-Za-z0-9_-]{43}");
    }

    static long instant(String value) {
        if (value == null || value.length() > 40 || !value.endsWith("Z")) throw new IllegalArgumentException("Invalid expiry");
        return Instant.parse(value).toEpochMilli();
    }

    static long positive(JSONObject object, String key) throws JSONException {
        Object value = object.get(key);
        if (!(value instanceof Integer || value instanceof Long) || ((Number) value).longValue() < 1)
            throw new JSONException("Invalid counter");
        return ((Number) value).longValue();
    }

    static JSONObject installation(JSONObject value) throws JSONException {
        Set<String> keys = new HashSet<>(Arrays.asList("id", "provider", "deviceLabel", "status", "bindingGeneration",
                "registrationVersion", "leaseExpiresAt", "lastPairingId", "lastSeenAt"));
        Set<String> actual = new HashSet<>();
        for (Iterator<String> it = value.keys(); it.hasNext();) actual.add(it.next());
        if (!actual.equals(keys) || !uuid(value.getString("id"))
                || !provider(value.getString("provider"))
                || !STATUSES.contains(value.getString("status"))
                || !bounded(value.getString("deviceLabel"), 256)
                || value.getString("deviceLabel").codePointCount(0, value.getString("deviceLabel").length()) > 64)
            throw new JSONException("Invalid installation");
        // A fresh pending installation has not received its first binding yet.
        Object generation = value.get("bindingGeneration");
        if (!(generation instanceof Integer || generation instanceof Long) || ((Number) generation).longValue() < 0)
            throw new JSONException("Invalid binding");
        positive(value, "registrationVersion");
        if (!value.isNull("leaseExpiresAt")) instant(value.getString("leaseExpiresAt"));
        if (!value.isNull("lastPairingId") && !uuid(value.getString("lastPairingId"))) throw new JSONException("Invalid pairing");
        instant(value.getString("lastSeenAt"));
        return new JSONObject(value.toString());
    }

    static JSONObject envelope(RemoteMessage message, long now) throws EnvelopeException {
        if (message == null) throw new EnvelopeException(EnvelopeReason.NULL_MESSAGE);
        if (!emptyNotification(message.getNotification())) throw new EnvelopeException(EnvelopeReason.NOTIFICATION_CONTENT);
        return envelope(message.getData(), now);
    }

    private static boolean empty(String value) { return value == null || value.isEmpty(); }

    private static boolean emptyNotification(Notification value) {
        return value == null || (empty(value.getTitle()) && empty(value.getBody()) && empty(value.getChannelId())
                && (value.getImageUrl() == null || value.getImageUrl().toString().isEmpty())
                && empty(value.getColor()) && empty(value.getIcon()) && empty(value.getClickAction())
                && (value.getClickActionType() == null || value.getClickActionType() == ClickActionType.DEFAULT));
    }

    private static String envelopeField(Map<String, String> data, String key, EnvelopeReason reason) throws EnvelopeException {
        Object value = data.get(key);
        if (!(value instanceof String)) throw new EnvelopeException(reason);
        return (String) value;
    }

    static JSONObject envelope(Map<String, String> data, long now) throws EnvelopeException {
        if (data == null || !data.keySet().equals(new HashSet<>(Arrays.asList(
                "v", "deliveryId", "installationId", "bindingGeneration", "expiresAt"))))
            throw new EnvelopeException(EnvelopeReason.DATA_KEYS);
        if (!Arrays.asList("1", "2").contains(envelopeField(data, "v", EnvelopeReason.VERSION)))
            throw new EnvelopeException(EnvelopeReason.VERSION);
        if (!uuid(envelopeField(data, "deliveryId", EnvelopeReason.DELIVERY_ID)))
            throw new EnvelopeException(EnvelopeReason.DELIVERY_ID);
        if (!uuid(envelopeField(data, "installationId", EnvelopeReason.INSTALLATION_ID)))
            throw new EnvelopeException(EnvelopeReason.INSTALLATION_ID);
        String generation = envelopeField(data, "bindingGeneration", EnvelopeReason.GENERATION);
        if (!generation.matches("[1-9][0-9]{0,18}"))
            throw new EnvelopeException(EnvelopeReason.GENERATION);
        try { Long.parseLong(generation); }
        catch (NumberFormatException invalid) { throw new EnvelopeException(EnvelopeReason.GENERATION); }
        long expiry;
        try { expiry = instant(envelopeField(data, "expiresAt", EnvelopeReason.EXPIRY_FORMAT)); }
        catch (RuntimeException invalid) { throw new EnvelopeException(EnvelopeReason.EXPIRY_FORMAT); }
        if (expiry <= now) throw new EnvelopeException(EnvelopeReason.EXPIRED);
        JSONObject value = new JSONObject(data);
        if (value.toString().getBytes(StandardCharsets.UTF_8).length > JSON_LIMIT)
            throw new EnvelopeException(EnvelopeReason.DATA_KEYS);
        return value;
    }

    static JSONObject delivery(JSONObject value, String id, long now) throws JSONException {
        if (!id.equals(value.getString("deliveryId")) || !uuid(value.getString("monitorId"))
                || !uuid(value.getString("incidentId")) || !Arrays.asList("down", "up").contains(value.getString("kind"))
                || !bounded(value.getString("title"), 400)
                || value.getString("title").codePointCount(0, value.getString("title").length()) > 100
                || !bounded(value.getString("body"), 2048)
                || value.getString("title").isEmpty() || instant(value.getString("expiresAt")) <= now)
            throw new JSONException("Invalid delivery");
        positive(value, "sequence");
        instant(value.getString("occurredAt"));
        return value;
    }

    private static boolean keys(JSONObject value, String... names) {
        Set<String> actual = new HashSet<>();
        for (Iterator<String> it = value.keys(); it.hasNext();) actual.add(it.next());
        return actual.equals(new HashSet<>(Arrays.asList(names)));
    }

    static JSONObject testDelivery(JSONObject value, String id, long now) throws JSONException {
        if (!keys(value, "deliveryId", "kind", "occurredAt", "expiresAt", "title", "body", "receiptToken")
                || !uuid(id) || !id.equals(value.getString("deliveryId")) || !"test".equals(value.getString("kind"))
                || !bounded(value.getString("title"), 400) || value.getString("title").isEmpty()
                || value.getString("title").codePointCount(0, value.getString("title").length()) > 100
                || !bounded(value.getString("body"), 2048) || !secret(value.getString("receiptToken")))
            throw new JSONException("Invalid test delivery");
        long occurred = instant(value.getString("occurredAt")), expiry = instant(value.getString("expiresAt"));
        if (occurred > now + 30000 || expiry <= now || expiry <= occurred || expiry - occurred > 5 * 60_000)
            throw new JSONException("Invalid test lifetime");
        return value;
    }

    static JSONObject nativeTest(JSONObject wrapper) throws JSONException {
        if (!keys(wrapper, "test")) throw new JSONException("Invalid test response");
        JSONObject value = wrapper.getJSONObject("test");
        if (!keys(value, "id", "state", "createdAt", "expiresAt", "nextAllowedAt", "receivedAt", "displaySubmittedAt", "errorCode")
                || !uuid(value.getString("id")) || !Arrays.asList("queued", "sending", "provider_accepted", "received",
                "display_submitted", "failed", "expired", "cancelled").contains(value.getString("state"))
                || !bounded(value.getString("errorCode"), 128)) throw new JSONException("Invalid test response");
        for (String name : new String[]{"createdAt", "expiresAt", "nextAllowedAt"}) instant(value.getString(name));
        for (String name : new String[]{"receivedAt", "displaySubmittedAt"}) if (!value.isNull(name)) instant(value.getString(name));
        return wrapper;
    }

    static String incidentUrl(JSONObject delivery) throws JSONException {
        String url = ORIGIN + "/app?monitor=" + delivery.getString("monitorId") + "&incident=" + delivery.getString("incidentId");
        if (!url.equals(LinkPolicy.appLink(url))) throw new JSONException("Invalid incident link");
        return url;
    }
}
