package ru.heartbeatchecker.app;

import org.json.JSONException;
import org.json.JSONObject;
import org.junit.Test;

import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.UUID;

import static org.junit.Assert.*;

public class DraftTest {
    private static final String ID = "12a45678-90ab-4cde-8f01-23456789abcd";

    @Test
    public void createAndJsonRoundTripPreserveUnicodeAndUrl() throws Exception {
        Draft draft = Draft.create("https://example.test/status?check=1", "Проверка 🚦");

        assertEquals(UUID.fromString(draft.id).toString(), draft.id);
        assertEquals("https://example.test/status?check=1", draft.url);
        assertEquals("Проверка 🚦", draft.name);
        Draft restored = Draft.fromJson(new JSONObject(draft.toJson().toString()));
        assertEquals(draft.id, restored.id);
        assertEquals(draft.url, restored.url);
        assertEquals(draft.name, restored.name);
    }

    @Test
    public void urlsAcceptNormalHttpAndHttpsButRejectUnsafeComponentsAndBounds() {
        assertTrue(Draft.validUrl("https://status.example.test/health?region=eu"));
        assertTrue(Draft.validUrl("HTTP://example.test:8080/health"));
        assertTrue(Draft.validUrl("https://example.test/path%20with%20spaces"));

        assertFalse(Draft.validUrl(null));
        assertFalse(Draft.validUrl(""));
        assertFalse(Draft.validUrl("//example.test/path"));
        assertFalse(Draft.validUrl("file:///etc/passwd"));
        assertFalse(Draft.validUrl("https://user:password@example.test/"));
        assertFalse(Draft.validUrl("https://example.test/path#secret"));
        assertFalse(Draft.validUrl("https://example.test:65536/"));
        assertFalse(Draft.validUrl("https://example.test/path\nheader"));
        assertFalse(Draft.validUrl("https://example.test/%00"));
        assertFalse(Draft.validUrl("https://example.test/%7f"));
        assertFalse(Draft.validUrl("https://example.test/" + "a".repeat(2040)));
        assertFalse(Draft.validUrl("https://example.test/" + '\uD800'));
    }

    @Test
    public void nameLimitCountsUnicodeCodePointsAndRejectsControlsAndInvalidSurrogates() {
        String hundredCodePoints = "🚀".repeat(100);
        Draft allowed = new Draft(ID, "https://example.test", hundredCodePoints);
        assertEquals(100, allowed.name.codePointCount(0, allowed.name.length()));

        assertThrows(IllegalArgumentException.class,
                () -> new Draft(ID, "https://example.test", hundredCodePoints + "x"));
        assertThrows(IllegalArgumentException.class,
                () -> new Draft(ID, "https://example.test", "line\nbreak"));
        assertThrows(IllegalArgumentException.class,
                () -> new Draft(ID, "https://example.test", "broken\uD800"));
        assertThrows(IllegalArgumentException.class,
                () -> new Draft("not-a-uuid", "https://example.test", "name"));
    }

    @Test
    public void jsonParserRequiresExactVersionTypesFieldsAndUuid() throws Exception {
        assertEquals("", Draft.fromJson(new JSONObject("{" +
                "\"v\":1,\"id\":\"" + ID + "\",\"url\":\"https://example.test\"}")).name);
        assertInvalidJson("{\"v\":1.0,\"id\":\"" + ID + "\",\"url\":\"https://example.test\"}");
        assertInvalidJson("{\"v\":2,\"id\":\"" + ID + "\",\"url\":\"https://example.test\"}");
        assertInvalidJson("{\"v\":1,\"id\":\"bad\",\"url\":\"https://example.test\"}");
        assertInvalidJson("{\"v\":1,\"id\":\"" + ID + "\",\"url\":\"https://example.test\",\"unexpected\":true}");
        assertInvalidJson("{\"v\":1,\"id\":\"" + ID + "\",\"url\":\"https://example.test\",\"name\":4}");
    }

    @Test
    public void launchUrlUsesUrlSafeUnpaddedBase64AndContainsStrictDraftJson() throws Exception {
        Draft draft = new Draft(ID, "https://example.test/health?check=1", "Открой 🚦");
        String launchUrl = draft.launchUrl();
        URI uri = URI.create(launchUrl);
        assertEquals(LinkPolicy.HOME, uri.toString().substring(0, uri.toString().indexOf('#')));
        String payload = uri.getRawFragment().substring("monitor-draft=".length());
        assertFalse(payload.contains("="));
        assertFalse(payload.contains("+"));
        assertFalse(payload.contains("/"));
        byte[] decoded = Base64.getUrlDecoder().decode(payload);
        assertTrue(decoded.length <= 4096);
        assertEquals(draft.toJson().toString(), new String(decoded, StandardCharsets.UTF_8));
        assertEquals(draft.id, Draft.fromJson(new JSONObject(new String(decoded, StandardCharsets.UTF_8))).id);
    }

    private static void assertInvalidJson(String raw) {
        try {
            Draft.fromJson(new JSONObject(raw));
            fail("accepted invalid draft JSON: " + raw);
        } catch (JSONException | IllegalArgumentException expected) {
            // Expected strict input rejection.
        }
    }
}
