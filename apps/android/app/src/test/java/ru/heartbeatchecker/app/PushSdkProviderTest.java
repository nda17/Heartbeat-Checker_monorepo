package ru.heartbeatchecker.app;

import org.json.JSONObject;
import org.junit.Test;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;

public final class PushSdkProviderTest {
    @Test
    public void selectedProviderStaysStickyAndLegacyTokenDefaultsToRuStore() throws Exception {
        assertEquals("fcm", PushSdk.provider(new JSONObject().put("provider", "fcm").put("token", "fcm-registration-id")));
        assertEquals("rustore", PushSdk.provider(new JSONObject().put("token", "legacy-1.1.3-token")));
        assertEquals("rustore", PushSdk.provider(new JSONObject().put("provider", "unknown").put("token", "legacy-token")));
    }

    @Test
    public void serverInstallationProviderIsUsedWhenLocalSelectionIsAbsent() throws Exception {
        JSONObject installation = new JSONObject().put("provider", "rustore").put("bindingGeneration", 4);
        assertEquals("rustore", PushSdk.provider(new JSONObject().put("installation", installation)));
    }

    @Test
    public void unboundInstallationCannotSelectProviderBeforeRegistration() throws Exception {
        JSONObject installation = new JSONObject().put("provider", "fcm").put("bindingGeneration", 0);
        assertEquals("", PushSdk.provider(new JSONObject().put("installation", installation)));
    }

    @Test
    public void huaweiIsNotConfiguredInThisRelease() {
        assertFalse(PushSdk.configured("huawei"));
    }
}
