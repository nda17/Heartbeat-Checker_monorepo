package ru.heartbeatchecker.app;

import org.json.JSONObject;
import org.junit.Test;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.assertFalse;

public final class PushSdkProviderTest {
    @Test
    public void selectedProviderStaysStickyAndLegacy113TokenKeepsRuStoreRoute() throws Exception {
        assertEquals("fcm", PushSdk.provider(new JSONObject().put("provider", "fcm")
                .put("token", "fcm-registration-id")));
        assertEquals("rustore", PushSdk.provider(new JSONObject().put("token", "legacy-1.1.3-token")));
        assertEquals("rustore", PushSdk.provider(new JSONObject().put("provider", "unknown")
                .put("token", "legacy-1.1.3-token")));
    }

    @Test
    public void serverInstallationProviderIsUsedWhenLocalSelectionIsAbsent() throws Exception {
        JSONObject installation = new JSONObject().put("provider", "rustore").put("bindingGeneration", 4);
        assertEquals("rustore", PushSdk.provider(new JSONObject().put("installation", installation)));
    }

    @Test
    public void unboundPendingInstallationCannotChooseProviderBeforeExchange() throws Exception {
        JSONObject installation = new JSONObject().put("provider", "fcm").put("bindingGeneration", 0);
        assertEquals("", PushSdk.provider(new JSONObject().put("installation", installation)));
    }

    @Test
    public void rustoreDistributorDiagnosticsAreScopedToTheSelectedProvider() throws Exception {
        JSONObject diagnostic = new JSONObject().put("providerDiagnostic",
                new JSONObject().put("code", "DISTRIBUTOR_MISSING"));
        for (String provider : new String[] {"fcm"}) {
            String message = PushSetupActivity.providerDiagnosticMessage(
                    new JSONObject(diagnostic.toString()).put("provider", provider));
            assertEquals("stale RuStore diagnostics are hidden for " + provider, "", message);
            assertFalse(message.contains("RuStore"));
        }
        String rustore = PushSetupActivity.providerDiagnosticMessage(
                new JSONObject(diagnostic.toString()).put("provider", "rustore"));
        assertTrue(rustore.contains("RuStore"));
    }

    @Test
    public void huaweiIsNotConfiguredInThisRelease() {
        assertFalse(PushSdk.configured("huawei"));
    }

    @Test
    public void genericSdkUnavailableDiagnosticDoesNotNameRuStore() throws Exception {
        String message = PushSetupActivity.providerDiagnosticMessage(new JSONObject().put("providerDiagnostic",
                new JSONObject().put("code", "SDK_UNAVAILABLE")));
        assertFalse(message.contains("RuStore"));
    }
}
