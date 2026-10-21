package ru.heartbeatchecker.app;

import java.net.URI;
import java.net.URISyntaxException;
import java.util.HashMap;
import java.util.Arrays;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.regex.Pattern;

final class LinkPolicy {
    static final String HOME = "https://heartbeat-checker.ru/app";
    private static final Set<String> PATHS = new HashSet<>(Arrays.asList("/app", "/app/settings", "/app/billing"));
    private static final Pattern UUID_PATTERN = Pattern.compile(
            "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}");

    private LinkPolicy() {}

    static String appLink(String value) {
        if (value == null || value.length() > 4096 || value.codePoints().anyMatch(c -> c < 32 || c == 127)) return null;
        try {
            URI uri = new URI(value);
            if (!"https".equals(uri.getScheme())
                    || !"heartbeat-checker.ru".equals(uri.getRawAuthority())
                    || !PATHS.contains(uri.getRawPath()) || uri.getRawFragment() != null) return null;
            String query = uri.getRawQuery();
            if (query == null) return "https://heartbeat-checker.ru" + uri.getRawPath();
            if (!"/app".equals(uri.getRawPath())) return null;
            Map<String, String> params = new HashMap<>();
            for (String part : query.split("&", -1)) {
                String[] pair = part.split("=", -1);
                if (pair.length != 2) return null;
                String key = pair[0];
                String item = pair[1];
                if (params.put(key, item) != null) return null;
            }
            if (!params.keySet().equals(new HashSet<>(Arrays.asList("monitor", "incident")))
                    || !UUID_PATTERN.matcher(params.get("monitor")).matches()
                    || !UUID_PATTERN.matcher(params.get("incident")).matches()) return null;
            return HOME + "?monitor=" + params.get("monitor") + "&incident=" + params.get("incident");
        } catch (URISyntaxException | IllegalArgumentException e) {
            return null;
        }
    }
}
