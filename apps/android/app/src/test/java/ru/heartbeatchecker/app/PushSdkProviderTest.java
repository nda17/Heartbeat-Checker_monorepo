package ru.heartbeatchecker.app;

import org.json.JSONObject;
import org.json.JSONArray;
import org.junit.Test;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

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
    public void selectedHuaweiRouteStaysStickyDespiteOtherProviderCandidates() throws Exception {
        JSONObject state = new JSONObject().put("provider", "huawei").put("token", "huawei-token")
                .put("candidate_fcm", "late-fcm-token").put("candidate_rustore", "late-rustore-token");
        assertEquals("huawei", PushSdk.provider(state));
    }

    @Test
    public void selectedProviderAvailabilityTreatsTransientStatesAsRetryable() {
        assertArrayEquals(new String[] {"fcm", "huawei", "rustore"}, PushSdk.providerOrder());
        assertEquals(PushSdk.Availability.READY, PushSdk.availability("huawei", 0));
        assertEquals(PushSdk.Availability.MISSING, PushSdk.availability("huawei", 1));
        assertEquals(PushSdk.Availability.RETRY, PushSdk.availability("huawei", 2));
        assertEquals(PushSdk.Availability.RETRY, PushSdk.availability("huawei", 9));
        assertEquals(PushSdk.Availability.MISSING, PushSdk.availability("fcm", 9));
        assertEquals(PushSdk.Availability.RETRY, PushSdk.availability("fcm", 3));
    }

    @Test
    public void nextFreshCandidateSkipsUnavailableRoutesAndHonorsServerAllowlist() throws Exception {
        JSONArray allAllowed = new JSONArray().put("fcm").put("huawei").put("rustore");
        assertEquals("Huawei is the next supported fresh route after unavailable FCM", 1,
                PushSdk.nextCandidate("", allAllowed, 0,
                        provider -> "fcm".equals(provider) ? false
                                : "huawei".equals(provider) && PushSdk.availability("huawei", 0) == PushSdk.Availability.READY));
        assertEquals("RuStore is the last route when Huawei is unavailable", 2,
                PushSdk.nextCandidate("", allAllowed, 0,
                        provider -> "fcm".equals(provider) ? false
                                : "huawei".equals(provider) ? PushSdk.availability("huawei", 1) == PushSdk.Availability.READY
                                : "rustore".equals(provider)));
        assertEquals("all-ready fresh devices start with FCM", 0,
                PushSdk.nextCandidate("", allAllowed, 0, provider -> true));
        assertEquals("the selected previous route cannot be selected again", -1,
                PushSdk.nextCandidate("fcm", allAllowed, 0, "fcm"::equals));
        assertEquals("a route omitted by the server cannot be selected", -1,
                PushSdk.nextCandidate("fcm", new JSONArray().put("fcm"), 0, "huawei"::equals));
    }

    @Test
    public void huaweiCallbacksAreIgnoredOutsideConfiguredMainProcess() {
        assertTrue(HuaweiPushService.acceptsCallbacks("ru.heartbeatchecker.app", "ru.heartbeatchecker.app", true));
        assertFalse(HuaweiPushService.acceptsCallbacks("ru.heartbeatchecker.app:pushservice", "ru.heartbeatchecker.app", true));
        assertFalse(HuaweiPushService.acceptsCallbacks(null, "ru.heartbeatchecker.app", true));
        assertFalse(HuaweiPushService.acceptsCallbacks("ru.heartbeatchecker.app", "ru.heartbeatchecker.app", false));
    }

    @Test
    public void isolatedDevAndSdkTestVariantsHaveNoHuaweiIdentity() {
        if (BuildConfig.DEV_CLIENT || BuildConfig.ISOLATED_API) {
            assertFalse(BuildConfig.HUAWEI_PUSH_ENABLED);
            assertEquals("", BuildConfig.HUAWEI_APP_ID);
            assertEquals("", BuildConfig.HUAWEI_PROJECT_ID);
            assertEquals("", BuildConfig.HUAWEI_CP_ID);
            assertFalse(PushSdk.configured("huawei"));
        }
    }
}
