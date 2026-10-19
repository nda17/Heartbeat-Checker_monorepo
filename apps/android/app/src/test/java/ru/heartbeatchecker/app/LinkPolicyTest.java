package ru.heartbeatchecker.app;

import org.junit.Test;

import static org.junit.Assert.*;

public class LinkPolicyTest {
    private static final String MONITOR = "123e4567-e89b-12d3-a456-426614174000";
    private static final String INCIDENT = "987e6543-e21b-12d3-a456-426614174000";

    @Test
    public void acceptsOnlyCanonicalAppPathsAndCanonicalizesIncidentPair() {
        assertEquals(LinkPolicy.HOME, LinkPolicy.appLink(LinkPolicy.HOME));
        assertEquals("https://heartbeat-checker.ru/app/settings",
                LinkPolicy.appLink("https://heartbeat-checker.ru/app/settings"));
        assertEquals(LinkPolicy.HOME + "?monitor=" + MONITOR + "&incident=" + INCIDENT,
                LinkPolicy.appLink(LinkPolicy.HOME + "?incident=" + INCIDENT + "&monitor=" + MONITOR));
    }

    @Test
    public void rejectsForeignOriginsCallbacksAndMalformedIncidentPairs() {
        assertNull(LinkPolicy.appLink("https://heartbeat-checker.ru.evil.test/app"));
        assertNull(LinkPolicy.appLink("https://heartbeat-checker.ru@evil.test/app"));
        assertNull(LinkPolicy.appLink("https://heartbeat-checker.ru:443/app"));
        assertNull(LinkPolicy.appLink("http://heartbeat-checker.ru/app"));
        assertNull(LinkPolicy.appLink("https://heartbeat-checker.ru/oauth/callback"));
        assertNull(LinkPolicy.appLink("https://heartbeat-checker.ru/app?code=secret&state=state"));
        assertNull(LinkPolicy.appLink(LinkPolicy.HOME + "?monitor=" + MONITOR));
        assertNull(LinkPolicy.appLink(LinkPolicy.HOME + "?monitor=" + MONITOR + "&monitor=" + MONITOR + "&incident=" + INCIDENT));
        assertNull(LinkPolicy.appLink(LinkPolicy.HOME + "?monitor=not-a-uuid&incident=" + INCIDENT));
        assertNull(LinkPolicy.appLink("https://heartbeat-checker.ru/app/settings?monitor=" + MONITOR + "&incident=" + INCIDENT));
        assertNull(LinkPolicy.appLink(LinkPolicy.HOME + "#monitor-draft=payload"));
        assertNull(LinkPolicy.appLink(null));
    }
}
