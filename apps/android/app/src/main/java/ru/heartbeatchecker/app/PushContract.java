package ru.heartbeatchecker.app;

import org.json.JSONException;
import org.json.JSONObject;

import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.Arrays;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.Iterator;
import java.util.regex.Pattern;

final class PushContract {
    static final String ORIGIN = "https://heartbeat-checker.ru";
    static final int JSON_LIMIT = 8192;
    private static final Pattern UUID = Pattern.compile("[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}");
    private static final Set<String> STATUSES = new HashSet<>(Arrays.asList("pending", "ready", "permission_denied",
            "session_expired", "session_revoked", "invalid_token", "provider_unavailable"));

    private PushContract() {}

    static boolean uuid(String value) { return value != null && UUID.matcher(value).matches(); }

    static boolean bounded(String value, int bytes) {
        return value != null && value.getBytes(StandardCharsets.UTF_8).length <= bytes
                && value.codePoints().noneMatch(c -> c < 32 || c == 127);
    }

    static boolean token(String value) { return value != null && !value.isEmpty() && bounded(value, 4096); }

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

    static String setupPair(String value) {
        try {
            URI uri = new URI(value);
            if (!"https".equals(uri.getScheme()) || !"heartbeat-checker.ru".equals(uri.getRawAuthority())
                    || uri.getRawFragment() != null) return null;
            if ("/android/push/connect".equals(uri.getRawPath()) && uri.getRawQuery() == null) return "";
            String query = uri.getRawQuery();
            if (!"/android/push/complete".equals(uri.getRawPath()) || query == null
                    || !query.startsWith("pairingId=")) return null;
            String id = query.substring("pairingId=".length());
            return uuid(id) ? id : null;
        } catch (Exception ignored) { return null; }
    }

    static boolean confirmation(String url, String id) {
        return uuid(id) && (ORIGIN + "/app/settings?pushPair=" + id).equals(url);
    }

    static JSONObject installation(JSONObject value) throws JSONException {
        Set<String> keys = new HashSet<>(Arrays.asList("id", "provider", "deviceLabel", "status", "bindingGeneration",
                "registrationVersion", "leaseExpiresAt", "lastPairingId", "lastSeenAt"));
        Set<String> actual = new HashSet<>();
        for (Iterator<String> it = value.keys(); it.hasNext();) actual.add(it.next());
        if (!actual.equals(keys) || !uuid(value.getString("id"))
                || !"rustore".equals(value.getString("provider"))
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

    static JSONObject envelope(Map<String, String> data, long now) throws JSONException {
        if (data == null || !data.keySet().equals(new HashSet<>(Arrays.asList(
                "v", "deliveryId", "installationId", "bindingGeneration", "expiresAt"))))
            throw new JSONException("Invalid envelope");
        if (!"1".equals(data.get("v")) || !uuid(data.get("deliveryId")) || !uuid(data.get("installationId")))
            throw new JSONException("Invalid envelope");
        String generation = data.get("bindingGeneration");
        if (generation == null || !generation.matches("[1-9][0-9]{0,18}")) throw new JSONException("Invalid generation");
        long parsed = Long.parseLong(generation);
        if (parsed < 1 || instant(data.get("expiresAt")) <= now) throw new JSONException("Expired envelope");
        JSONObject value = new JSONObject(data);
        if (value.toString().getBytes(StandardCharsets.UTF_8).length > JSON_LIMIT) throw new JSONException("Oversized envelope");
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

    static String incidentUrl(JSONObject delivery) throws JSONException {
        String url = ORIGIN + "/app?monitor=" + delivery.getString("monitorId") + "&incident=" + delivery.getString("incidentId");
        if (!url.equals(LinkPolicy.appLink(url))) throw new JSONException("Invalid incident link");
        return url;
    }
}
