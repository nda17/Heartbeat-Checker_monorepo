package ru.heartbeatchecker.app;

import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;
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
    public void existingRoutesUpgradeOnlyToReadyHigherPriorityAllowlistedProvider() {
        JSONArray allAllowed = new JSONArray().put("fcm").put("huawei").put("rustore");
        assertEquals("RuStore upgrades to ready FCM", 0,
                PushSdk.nextPreferredCandidate("rustore", allAllowed, 0,
                        provider -> "fcm".equals(provider)));
        assertEquals("RuStore skips unavailable FCM and upgrades to Huawei", 1,
                PushSdk.nextPreferredCandidate("rustore", allAllowed, 0,
                        provider -> "huawei".equals(provider)));
        assertEquals("Huawei upgrades to ready FCM", 0,
                PushSdk.nextPreferredCandidate("huawei", allAllowed, 0,
                        "fcm"::equals));
        assertEquals("an unavailable higher route leaves Huawei unchanged", -1,
                PushSdk.nextPreferredCandidate("huawei", allAllowed, 0, provider -> false));
        assertEquals("the current route must remain in the server allowlist", -1,
                PushSdk.nextPreferredCandidate("rustore", new JSONArray().put("fcm").put("huawei"), 0,
                        provider -> true));
        assertEquals("a disallowed higher route is never selected", -1,
                PushSdk.nextPreferredCandidate("rustore", new JSONArray().put("rustore"), 0,
                        provider -> true));
    }

    @Test
    public void providerTokenFailureIsTerminalForFreshAndCurrentRoutes() {
        JSONArray allAllowed = new JSONArray().put("fcm").put("huawei").put("rustore");
        List<String> freshRequests = new ArrayList<>();
        List<PushSdk.TokenResult> freshCallbacks = new ArrayList<>();
        RecordingTokenResult freshResult = new RecordingTokenResult(true);
        PushSdk.requestBestProvider("", allAllowed, false, provider -> true, freshResult,
                (provider, callback) -> { freshRequests.add(provider); freshCallbacks.add(callback); });
        assertEquals("a fresh all-ready device requests only its highest-priority provider",
                java.util.Collections.singletonList("fcm"), freshRequests);
        freshCallbacks.get(0).unavailable();
        assertEquals("fresh token failure is terminal", 1, freshResult.unavailable);
        assertEquals("fresh token failure does not fall through to Huawei or RuStore",
                java.util.Collections.singletonList("fcm"), freshRequests);

        List<String> existingRequests = new ArrayList<>();
        List<PushSdk.TokenResult> existingCallbacks = new ArrayList<>();
        RecordingTokenResult existingResult = new RecordingTokenResult(true);
        PushSdk.requestBestProvider("rustore", allAllowed, true, provider -> true, existingResult,
                (provider, callback) -> { existingRequests.add(provider); existingCallbacks.add(callback); });
        assertEquals("a valid RuStore route tries only its ready FCM upgrade",
                java.util.Collections.singletonList("fcm"), existingRequests);
        existingCallbacks.get(0).unavailable();
        assertEquals("failed upgrade is terminal and does not request lower candidates",
                java.util.Collections.singletonList("fcm"), existingRequests);
        assertEquals(1, existingResult.unavailable);

        List<String> unavailableFcmRequests = new ArrayList<>();
        PushSdk.requestBestProvider("rustore", allAllowed, true, provider -> "huawei".equals(provider),
                new RecordingTokenResult(true), (provider, callback) -> unavailableFcmRequests.add(provider));
        assertEquals("an unavailable FCM allows the next ready higher route",
                java.util.Collections.singletonList("huawei"), unavailableFcmRequests);
    }

    @Test
    public void inactiveTokenAttemptCannotDispatchAProviderRequest() {
        List<String> requests = new ArrayList<>();
        RecordingTokenResult result = new RecordingTokenResult(false);
        PushSdk.requestBestProvider("rustore", new JSONArray().put("fcm").put("huawei").put("rustore"), true,
                provider -> true, result, (provider, callback) -> requests.add(provider));
        assertTrue("a timed out or cancelled token attempt cannot start an SDK request", requests.isEmpty());
        assertEquals("the expired attempt is reported unavailable", 1, result.unavailable);
    }

    @Test
    public void delayedSdkStageIsFencedAfterTimeoutOrOwnerChange() {
        AtomicInteger stageRequests = new AtomicInteger();
        RecordingTokenResult timedOut = new RecordingTokenResult(false);
        PushSdk.requestTokenStage(() -> true, timedOut, stageRequests::incrementAndGet);
        assertEquals("a late prior-stage callback cannot start a new SDK stage after timeout", 0, stageRequests.get());
        assertEquals(1, timedOut.unavailable);

        RecordingTokenResult activeResult = new RecordingTokenResult(true);
        PushSdk.requestTokenStage(() -> false, activeResult, stageRequests::incrementAndGet);
        assertEquals("a changed binding owner cannot start the next SDK stage", 0, stageRequests.get());
        assertEquals(1, activeResult.unavailable);
    }

    private static final class RecordingTokenResult implements PushSdk.TokenResult {
        private final boolean active;
        private int unavailable;

        RecordingTokenResult(boolean active) { this.active = active; }
        @Override public boolean active() { return active; }
        @Override public void ready(String token) { }
        @Override public void unavailable() { unavailable++; }
    }

    @Test
    public void candidateCommitSnapshotRejectsLogoutAndBindingRaces() throws Exception {
        JSONObject before = candidateState();
        JSONObject unchanged = new JSONObject(before.toString());
        assertTrue(PushSdk.candidateMatches(before, unchanged, 7, 7, true));
        assertFalse("logout/cancel invalidates the attempt", PushSdk.candidateMatches(before, unchanged, 7, 7, false));
        assertFalse("local revocation invalidates the attempt", PushSdk.candidateMatches(before, unchanged, 7, 8, true));

        JSONObject changed = new JSONObject(before.toString()).put("accountId", "33333333-3333-4333-8333-333333333333");
        assertFalse("an account switch invalidates the attempt", PushSdk.candidateMatches(before, changed, 7, 7, true));
        changed = new JSONObject(before.toString()).put("sessionId", "44444444-4444-4444-8444-444444444444");
        assertFalse("a session switch invalidates the attempt", PushSdk.candidateMatches(before, changed, 7, 7, true));
        changed = new JSONObject(before.toString()).put("pendingBind", new JSONObject());
        assertFalse("a pending bind invalidates the attempt", PushSdk.candidateMatches(before, changed, 7, 7, true));
        changed = new JSONObject(before.toString()).put("optedOut", true);
        assertFalse("opt-out invalidates the attempt", PushSdk.candidateMatches(before, changed, 7, 7, true));
        changed = new JSONObject(before.toString()).put("tokenRevision", 8);
        assertFalse("a token rotation invalidates a stale candidate", PushSdk.candidateMatches(before, changed, 7, 7, true));
        changed = new JSONObject(before.toString());
        changed.getJSONObject("installation").put("bindingGeneration", 5);
        assertFalse("a rebind invalidates the attempt", PushSdk.candidateMatches(before, changed, 7, 7, true));
    }

    private static JSONObject candidateState() throws Exception {
        return new JSONObject().put("provider", "rustore").put("token", "existing-rustore-token")
                .put("tokenRevision", 7).put("accountId", "11111111-1111-4111-8111-111111111111")
                .put("sessionId", "22222222-2222-4222-8222-222222222222")
                .put("enabled", true).put("optedOut", false).put("credential", "credential")
                .put("installation", new JSONObject().put("id", "installation-id")
                        .put("bindingGeneration", 4).put("registrationVersion", 3));
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
