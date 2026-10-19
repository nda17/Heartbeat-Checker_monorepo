package ru.heartbeatchecker.app;

import org.json.JSONException;
import org.json.JSONObject;

import java.net.URI;
import java.net.URISyntaxException;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.Arrays;
import java.util.HashSet;
import java.util.Iterator;
import java.util.Set;
import java.util.UUID;
import java.util.regex.Pattern;

final class Draft {
    private static final Pattern UUID_PATTERN = Pattern.compile(
            "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}");
    private static final Pattern ENCODED_CONTROL = Pattern.compile("%([01][0-9a-fA-F]|7[fF])");
    private static final Set<String> FIELDS = new HashSet<>(Arrays.asList("v", "id", "url", "name"));

    final String id;
    final String url;
    final String name;

    Draft(String id, String url, String name) {
        if (id == null || !UUID_PATTERN.matcher(id).matches() || !validUrl(url)
                || name == null || name.codePointCount(0, name.length()) > 100
                || hasControl(name) || hasInvalidSurrogate(name)) {
            throw new IllegalArgumentException("Некорректный черновик");
        }
        this.id = id;
        this.url = url;
        this.name = name;
    }

    static Draft create(String url, String name) {
        return new Draft(UUID.randomUUID().toString(), url, name);
    }

    static boolean validUrl(String value) {
        if (value == null || value.isEmpty() || value.getBytes(StandardCharsets.UTF_8).length > 2048
                || hasControl(value) || hasInvalidSurrogate(value)
                || ENCODED_CONTROL.matcher(value).find()) return false;
        try {
            URI uri = new URI(value);
            return ("https".equalsIgnoreCase(uri.getScheme()) || "http".equalsIgnoreCase(uri.getScheme()))
                    && uri.getHost() != null && !uri.getHost().isEmpty()
                    && uri.getRawUserInfo() == null && uri.getRawFragment() == null
                    && uri.getPort() >= -1 && uri.getPort() <= 65535;
        } catch (URISyntaxException e) {
            return false;
        }
    }

    static boolean hasControl(String value) {
        return value.codePoints().anyMatch(c -> c < 32 || c == 127);
    }

    private static boolean hasInvalidSurrogate(String value) {
        for (int i = 0; i < value.length(); i++) {
            char c = value.charAt(i);
            if (Character.isHighSurrogate(c)) {
                if (++i >= value.length() || !Character.isLowSurrogate(value.charAt(i))) return true;
            } else if (Character.isLowSurrogate(c)) return true;
        }
        return false;
    }

    JSONObject toJson() throws JSONException {
        JSONObject json = new JSONObject().put("v", 1).put("id", id).put("url", url);
        if (!name.isEmpty()) json.put("name", name);
        return json;
    }

    static Draft fromJson(JSONObject json) throws JSONException {
        Iterator<String> keys = json.keys();
        while (keys.hasNext()) {
            if (!FIELDS.contains(keys.next())) throw new JSONException("Unknown draft field");
        }
        Object version = json.get("v");
        if (!(version instanceof Integer) || ((Integer) version) != 1
                || !(json.get("id") instanceof String) || !(json.get("url") instanceof String)
                || (json.has("name") && !(json.get("name") instanceof String))) {
            throw new JSONException("Invalid draft fields");
        }
        return new Draft(json.getString("id"), json.getString("url"), json.optString("name", ""));
    }

    String launchUrl() throws JSONException {
        byte[] bytes = toJson().toString().getBytes(StandardCharsets.UTF_8);
        if (bytes.length > 4096) throw new IllegalArgumentException("Черновик слишком большой");
        return LinkPolicy.HOME + "#monitor-draft=" + Base64.getUrlEncoder().withoutPadding().encodeToString(bytes);
    }
}
