package androidreadiness

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const launcherPackage = "com.google.android.apps.nexuslauncher"
const launcherComponent = launcherPackage + "/.NexusLauncherActivity"

type outcome struct {
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	RecoveryCount  int    `json:"recoveryCount"`
	StableSeconds  int    `json:"stableSeconds"`
	ElapsedSeconds int    `json:"elapsedSeconds"`
	Samples        int    `json:"samples"`
	ExitCode       int    `json:"-"`
	TimeoutValues  []int  `json:"-"`
}

func readinessScript(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate readiness test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	return filepath.Join(root, "deploy", "android", "emulator-readiness.sh")
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}

func newHarness(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	fixtures := filepath.Join(root, "fixtures")
	report := filepath.Join(root, "reports")
	for _, dir := range []string{bin, fixtures, report} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	clock := filepath.Join(root, "clock")
	samples := filepath.Join(root, "samples")
	if err := os.WriteFile(clock, []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(samples, []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}

	writeExecutable(t, filepath.Join(bin, "date"), `#!/usr/bin/env bash
set -euo pipefail
[[ ${1:-} == +%s ]] || exit 2
cat "$FAKE_CLOCK"
`)
	writeExecutable(t, filepath.Join(bin, "sleep"), `#!/usr/bin/env bash
set -euo pipefail
step=${FAKE_SLEEP_STEP:-${1:-1}}
now=$(cat "$FAKE_CLOCK")
printf '%s\n' "$((now + step))" > "$FAKE_CLOCK"
`)
	// The fake timeout preserves the helper's actual command and remaining-budget
	// argument while avoiding host-specific GNU timeout availability in tests.
	writeExecutable(t, filepath.Join(bin, "timeout"), `#!/usr/bin/env bash
set -euo pipefail
if [[ ${1:-} == --kill-after=1s ]]; then shift; fi
limit=${1:?missing timeout}
printf 'timeout:%s\n' "$limit" >> "$TIMEOUT_LOG"
shift
if [[ ${FAKE_MODE:-} == command-timeout ]]; then exit 124; fi
exec "$@"
`)
	writeExecutable(t, filepath.Join(bin, "cp"), `#!/usr/bin/env bash
set -euo pipefail
if [[ ${FAKE_MODE:-} == snapshot-copy-failure ]]; then exit 1; fi
exec /bin/cp "$@"
`)
	writeExecutable(t, filepath.Join(bin, "adb"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$ADB_CALLS"
[[ ${1:-} == -s && ${2:-} == emulator-fixture ]] || exit 2
shift 2
if [[ ${1:-} == shell && ${2:-} == cmd && ${3:-} == package && ${4:-} == resolve-activity ]]; then
  sample=$(cat "$FAKE_SAMPLES")
  sample=$((sample + 1))
  printf '%s\n' "$sample" > "$FAKE_SAMPLES"
  case "$FAKE_MODE:$sample" in
	    delayed:1|delayed:2|waiting:*) state=waiting ;;
	    setup-transition:1) state=setup-fallback ;;
	    setup-transition:*) state=ready ;;
	    setup-permanent:*) state=setup-fallback ;;
	    exact-anr:1|repeated-anr:1|repeated-anr:2|repeated-anr:3|post-recovery-unknown:1|force-stop-failure:1|home-start-failure:1) state=launcher-anr ;;
	    post-recovery-unknown:2) state=unknown-anr ;;
    unknown-anr:1) state=unknown-anr ;;
    system-error:1) state=system-error ;;
    historical-anr:*) state=historical-anr ;;
    malformed:1) state=malformed ;;
    wrong-display:1) state=wrong-display ;;
	    wrong-home:*) state=wrong-home ;;
	    wrong-home-anr:1) state=wrong-home-anr ;;
	    malformed-home:1) state=malformed-home ;;
    malformed-window-id:1) state=malformed-window-id ;;
    mixed-window:*) state=mixed-window ;;
    command-timeout:*) exit 124 ;;
    *) state=ready ;;
  esac
  printf '%s\n' "$state" > "$FAKE_STATE"
  cat "$FAKE_FIXTURES/$state-home.txt"
  exit 0
fi
if [[ ${1:-} == shell && ${2:-} == dumpsys ]]; then
  case "${3:-}:${4:-}" in
    power:) part=power ;;
    window:policy) part=policy ;;
    activity:activities) part=activities ;;
    window:displays) part=displays ;;
    window:windows) part=windows ;;
    activity:lastanr)
      printf 'ANR time: prior boot\nReason: stale historical launcher incident\n'
      exit 0
      ;;
    input:*) printf 'Input Dispatcher State: fixture\n'; exit 0 ;;
    *) exit 2 ;;
  esac
  state=$(cat "$FAKE_STATE")
	if [[ $state == malformed && $part == policy ]]; then
    printf 'KeyguardServiceDelegate:\n  showing=false\n'
    exit 0
  fi
	if [[ $FAKE_MODE == slow-first && $part == windows && $(cat "$FAKE_SAMPLES") == 1 ]]; then
		now=$(cat "$FAKE_CLOCK")
		printf '%s\n' "$((now + 10))" > "$FAKE_CLOCK"
	fi
  cat "$FAKE_FIXTURES/$state-$part.txt"
	if [[ $FAKE_MODE == repeated-anr && $part == windows ]]; then
		printf 'fixture_sample=%s\n' "$(cat "$FAKE_SAMPLES")"
	fi
  exit 0
fi
if [[ ${1:-} == shell && ${2:-} == am && ${3:-} == force-stop && ${FAKE_MODE:-} == force-stop-failure ]]; then exit 1; fi
if [[ ${1:-} == shell && ${2:-} == am && ${3:-} == force-stop ]]; then exit 0; fi
if [[ ${1:-} == shell && ${2:-} == am && ${3:-} == start && ${FAKE_MODE:-} == home-start-failure ]]; then exit 1; fi
if [[ ${1:-} == shell && ${2:-} == am && ${3:-} == start ]]; then printf 'Status: ok\n'; exit 0; fi
if [[ ${1:-} == logcat ]]; then printf 'logcat fixture\n'; exit 0; fi
exit 2
`)
	for _, state := range []string{"ready", "waiting", "setup-fallback", "launcher-anr", "unknown-anr", "system-error", "historical-anr", "malformed", "wrong-display", "wrong-home", "wrong-home-anr", "malformed-home", "malformed-window-id", "mixed-window"} {
		writeStateFixtures(t, fixtures, state)
	}
	return root, report, bin
}

func writeStateFixtures(t *testing.T, dir, state string) {
	t.Helper()
	home := launcherComponent + "\n"
	power := "mWakefulness=Awake\nmWakefulnessChanging=false\n"
	policy := "KeyguardServiceDelegate:\n  showing=false\n  dreaming=false\n  screenState=SCREEN_STATE_ON\n  interactiveState=INTERACTIVE_STATE_AWAKE\n"
	activities := "ResumedActivity: ActivityRecord{abc u0 " + launcherComponent + " t1}\n"
	displays := "Display: mDisplayId=0\n  mCurrentFocus=Window{abc123 u0 " + launcherComponent + "}\n  mFocusedApp=ActivityRecord{abc u0 " + launcherComponent + " t1}\n"
	window := "Window #0 Window{abc123 u0 " + launcherComponent + "}:\n" +
		"  mDisplayId=0 rootTaskId=1 mSession=Session{1 563:10180}\n" +
		"  mOwnerUid=10180 showForAllUsers=false package=" + launcherPackage + " appop=NONE\n" +
		"  mViewVisibility=0x0\n  mHasSurface=true isReadyForDisplay()=true\n" +
		"  Surface: shown=true      mDrawState=HAS_DRAWN       mLastHidden=false\n" +
		"  isOnScreen=true\n  isVisible=true\n"
	switch state {
	case "waiting":
		power = "mWakefulness=Asleep\nmWakefulnessChanging=false\n"
	case "setup-fallback":
		home = "priority=3 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=true\ncom.google.android.googlesdksetup/.DefaultActivity\n"
		activities = "ResumedActivity: ActivityRecord{deae9ae u0 com.android.settings/.FallbackHome t5}\n"
		displays = "Display: mDisplayId=0\n  mCurrentFocus=Window{7bdd436 u0 com.android.settings/com.android.settings.FallbackHome}\n  mFocusedApp=ActivityRecord{7e6effb u0 com.android.settings/.FallbackHome t5}\n"
		window = "Window #0 Window{7bdd436 u0 com.android.settings/com.android.settings.FallbackHome}:\n" +
			"  mDisplayId=0 rootTaskId=1 mSession=Session{33b412b 924:u0a10181}\n" +
			"  mOwnerUid=1000 showForAllUsers=false package=com.android.settings appop=NONE\n" +
			"  mViewVisibility=0x0\n  mHasSurface=true isReadyForDisplay()=true\n" +
			"  Surface: shown=true mDrawState=HAS_DRAWN mLastHidden=false\n" +
			"  isOnScreen=true\n  isVisible=true\n"
	case "wrong-home":
		home = "com.example.other/.MainActivity\n"
	case "malformed-home":
		home = "com.example.invalid\n"
	case "wrong-display":
		displays = "Display: mDisplayId=1\n  mCurrentFocus=Window{abc123 u0 " + launcherComponent + "}\n  mFocusedApp=ActivityRecord{abc u0 " + launcherComponent + " t1}\n"
	case "malformed-window-id":
		displays = "Display: mDisplayId=0\n  mCurrentFocus=Window{g1 u0 " + launcherComponent + "}\n  mFocusedApp=ActivityRecord{abc u0 " + launcherComponent + " t1}\n"
		window = strings.Replace(window, "abc123", "g1", 1)
	case "mixed-window":
		window = "Window #0 Window{abc123 u0 " + launcherComponent + "}:\n" +
			"  mDisplayId=0 rootTaskId=1 mSession=Session{1 563:10180}\n" +
			"  mOwnerUid=10180 showForAllUsers=false package=" + launcherPackage + " appop=NONE\n" +
			"  mViewVisibility=0x4\n  mHasSurface=false isReadyForDisplay()=false\n" +
			"  isOnScreen=false\n  isVisible=false\n" +
			"Window #2 Window{def456 u0 " + launcherComponent + "}:\n" +
			"  mDisplayId=0 rootTaskId=1 mSession=Session{2 563:10180}\n" +
			"  mOwnerUid=10180 showForAllUsers=false package=" + launcherPackage + " appop=NONE\n" +
			"  mViewVisibility=0x0\n  mHasSurface=true isReadyForDisplay()=true\n" +
			"  Surface: shown=true      mDrawState=HAS_DRAWN       mLastHidden=false\n" +
			"  isOnScreen=true\n  isVisible=true\n"
	case "launcher-anr":
		displays = "Display: mDisplayId=0\n  mCurrentFocus=Window{def456 u0 Application Not Responding: " + launcherPackage + "}\n  mFocusedApp=ActivityRecord{abc u0 " + launcherComponent + " t1}\n"
		window += "Window #1 Window{def456 u0 Application Not Responding: " + launcherPackage + "}:\n" +
			"  mDisplayId=0 rootTaskId=1 mSession=Session{2 563:1000}\n" +
			"  mOwnerUid=1000 showForAllUsers=true package=android appop=SYSTEM_ALERT_WINDOW\n" +
			"  mViewVisibility=0x0\n  mHasSurface=true isReadyForDisplay()=true\n" +
			"  Surface: shown=true      mDrawState=HAS_DRAWN       mLastHidden=false\n" +
			"  isOnScreen=true\n  isVisible=true\n"
	case "wrong-home-anr":
		home = "com.example.other/.MainActivity\n"
		displays = "Display: mDisplayId=0\n  mCurrentFocus=Window{def456 u0 Application Not Responding: " + launcherPackage + "}\n  mFocusedApp=ActivityRecord{abc u0 " + launcherComponent + " t1}\n"
		window += "Window #1 Window{def456 u0 Application Not Responding: " + launcherPackage + "}:\n" +
			"  mDisplayId=0 rootTaskId=1 mSession=Session{2 563:1000}\n" +
			"  mOwnerUid=1000 showForAllUsers=true package=android appop=SYSTEM_ALERT_WINDOW\n" +
			"  mViewVisibility=0x0\n  mHasSurface=true isReadyForDisplay()=true\n" +
			"  Surface: shown=true mDrawState=HAS_DRAWN mLastHidden=false\n" +
			"  isOnScreen=true\n  isVisible=true\n"
	case "unknown-anr":
		displays = "Display: mDisplayId=0\n  mCurrentFocus=Window{def456 u0 Application Not Responding: com.example.other}\n  mFocusedApp=ActivityRecord{abc u0 " + launcherComponent + " t1}\n"
		window += "Window #1 Window{def456 u0 Application Not Responding: com.example.other}:\n" +
			"  mDisplayId=0 rootTaskId=1 mSession=Session{2 563:1000}\n" +
			"  mOwnerUid=1000 showForAllUsers=true package=android appop=SYSTEM_ALERT_WINDOW\n" +
			"  mViewVisibility=0x0\n  mHasSurface=true isReadyForDisplay()=true\n" +
			"  Surface: shown=true      mDrawState=HAS_DRAWN       mLastHidden=false\n" +
			"  isOnScreen=true\n  isVisible=true\n"
	case "system-error":
		window += "  mAttrs={(0,0)(fillxfill) ty=SYSTEM_ERROR fl=SHOW_FOR_ALL_USERS}\n"
	case "historical-anr":
		window += "Window #1 Window{def456 u0 Application Not Responding: " + launcherPackage + "}:\n" +
			"  mDisplayId=0 rootTaskId=1 mSession=Session{2 563:1000}\n" +
			"  mOwnerUid=1000 showForAllUsers=true package=android appop=SYSTEM_ALERT_WINDOW\n" +
			"  mViewVisibility=0x4\n  mHasSurface=false isReadyForDisplay()=false\n" +
			"  isOnScreen=false\n  isVisible=false\n"
	}
	for name, value := range map[string]string{
		"home": home, "power": power, "policy": policy, "activities": activities,
		"displays": displays, "windows": window,
	} {
		if err := os.WriteFile(filepath.Join(dir, state+"-"+name+".txt"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func runHelper(t *testing.T, mode string) (outcome, string, string, string) {
	t.Helper()
	root, reports, bin := newHarness(t)
	env := append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"FAKE_MODE="+mode,
		"FAKE_FIXTURES="+filepath.Join(root, "fixtures"),
		"FAKE_STATE="+filepath.Join(root, "state"),
		"FAKE_SAMPLES="+filepath.Join(root, "samples"),
		"FAKE_CLOCK="+filepath.Join(root, "clock"),
		"ADB_CALLS="+filepath.Join(root, "adb-calls"),
		"TIMEOUT_LOG="+filepath.Join(root, "timeout-calls"),
	)
	if mode == "waiting" || mode == "mixed-window" || mode == "setup-permanent" || mode == "wrong-home" {
		env = append(env, "FAKE_SLEEP_STEP=60")
	}
	cmd := exec.Command("bash", readinessScript(t), filepath.Join(bin, "adb"), "emulator-fixture", reports)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("run readiness helper: %v", err)
		}
	}
	data, readErr := os.ReadFile(filepath.Join(reports, "readiness", "outcome.json"))
	if readErr != nil {
		t.Fatalf("read readiness outcome (exit %d, output %s): %v", code, output, readErr)
	}
	var got outcome
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode readiness outcome %q: %v", data, err)
	}
	got.ExitCode = code
	if timeoutData, err := os.ReadFile(filepath.Join(root, "timeout-calls")); err == nil {
		for _, line := range strings.Fields(string(timeoutData)) {
			if strings.HasPrefix(line, "timeout:") {
				valueText := strings.TrimSuffix(strings.TrimPrefix(line, "timeout:"), "s")
				if value, err := strconv.Atoi(valueText); err == nil {
					got.TimeoutValues = append(got.TimeoutValues, value)
				}
			}
		}
	}
	calls, _ := os.ReadFile(filepath.Join(root, "adb-calls"))
	return got, string(calls), string(output), reports
}

func assertOutcome(t *testing.T, got outcome, status, reason string, recoveryCount int) {
	t.Helper()
	if got.Status != status || got.Reason != reason || got.RecoveryCount != recoveryCount {
		t.Fatalf("outcome = %+v; want status=%q reason=%q recoveryCount=%d", got, status, reason, recoveryCount)
	}
	if status == "ready" && got.ExitCode != 0 {
		t.Fatalf("ready outcome exited with %d; want zero", got.ExitCode)
	}
	if status != "ready" && got.ExitCode == 0 {
		t.Fatal("failed outcome exited zero")
	}
}

func countCallLog(calls, needle string) int {
	return strings.Count(calls, needle)
}

func TestReadyWaitsForStableHomeAndAcceptsDelayedBoot(t *testing.T) {
	for _, mode := range []string{"ready", "delayed"} {
		t.Run(mode, func(t *testing.T) {
			got, _, _, _ := runHelper(t, mode)
			assertOutcome(t, got, "ready", "home_stable", 0)
			if got.StableSeconds < 6 || got.ElapsedSeconds > 120 {
				t.Fatalf("readiness interval outside bounds: %+v", got)
			}
		})
	}
}

func TestProvisioningHomeWaitsThenAcceptsNexusLauncher(t *testing.T) {
	got, calls, _, reports := runHelper(t, "setup-transition")
	assertOutcome(t, got, "ready", "home_stable", 0)
	if got.Samples < 8 || got.StableSeconds < 6 || got.ElapsedSeconds > 120 {
		t.Fatalf("provisioning state was not followed by a full stable Nexus interval: %+v", got)
	}
	if strings.Contains(calls, "shell am force-stop "+launcherPackage) {
		t.Fatalf("provisioning transition must not trigger launcher recovery:\n%s", calls)
	}
	finalHome, err := os.ReadFile(filepath.Join(reports, "readiness", "final", "home.txt"))
	if err != nil {
		t.Fatalf("read final HOME resolver snapshot: %v", err)
	}
	if !strings.Contains(string(finalHome), launcherComponent) {
		t.Fatalf("readiness completed without exact Nexus HOME resolver:\n%s", finalHome)
	}
}

func TestExactLauncherANRRecoversOnceThenReachesReady(t *testing.T) {
	got, calls, _, _ := runHelper(t, "exact-anr")
	assertOutcome(t, got, "ready", "home_stable", 1)
	if countCallLog(calls, "shell am force-stop "+launcherPackage) != 1 {
		t.Fatalf("launcher force-stop count is not one:\n%s", calls)
	}
	if countCallLog(calls, "shell am start -W -a android.intent.action.MAIN -c android.intent.category.HOME -n "+launcherComponent) != 1 {
		t.Fatalf("HOME restart count is not one:\n%s", calls)
	}
}

func TestRecoveryCommandFailuresStopBeforeInstrumentationReadiness(t *testing.T) {
	for _, mode := range []string{"force-stop-failure", "home-start-failure"} {
		t.Run(mode, func(t *testing.T) {
			got, calls, _, _ := runHelper(t, mode)
			assertOutcome(t, got, "failed", "recovery_failed", 1)
			if countCallLog(calls, "shell am force-stop "+launcherPackage) != 1 {
				t.Fatalf("expected one launcher force-stop attempt:\n%s", calls)
			}
			starts := countCallLog(calls, "shell am start -W -a android.intent.action.MAIN")
			if mode == "force-stop-failure" && starts != 0 {
				t.Fatalf("HOME relaunch ran after force-stop failed:\n%s", calls)
			}
			if mode == "home-start-failure" && starts != 1 {
				t.Fatalf("expected one HOME relaunch attempt:\n%s", calls)
			}
		})
	}
}

func TestSnapshotCopyFailureStopsBeforeRecoveryAndReturnsError(t *testing.T) {
	got, calls, _, _ := runHelper(t, "snapshot-copy-failure")
	assertOutcome(t, got, "failed", "diagnostics_unreadable", 0)
	if strings.Contains(calls, "shell am force-stop "+launcherPackage) {
		t.Fatalf("failed snapshot copy must stop before recovery:\n%s", calls)
	}
}

func TestFirstReadyIntervalStartsAfterFirstClassifiedReadySample(t *testing.T) {
	got, _, _, _ := runHelper(t, "slow-first")
	assertOutcome(t, got, "ready", "home_stable", 0)
	if got.ElapsedSeconds < 16 || got.Samples < 7 {
		t.Fatalf("slow initial collection was counted as ready time: %+v", got)
	}
}

func TestUnknownAndRepeatedActiveErrorsFailClosed(t *testing.T) {
	tests := []struct {
		mode, reason string
		recoveries   int
	}{
		{"unknown-anr", "unknown_active_error", 0},
		{"system-error", "unknown_active_error", 0},
		{"repeated-anr", "repeated_launcher_anr", 1},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			got, calls, _, reports := runHelper(t, tc.mode)
			assertOutcome(t, got, "failed", tc.reason, tc.recoveries)
			if countCallLog(calls, "shell am force-stop "+launcherPackage) != tc.recoveries {
				t.Fatalf("unexpected recovery calls:\n%s", calls)
			}
			if tc.mode == "repeated-anr" {
				firstSnapshot, err := os.ReadFile(filepath.Join(reports, "readiness", "before-recovery", "windows.txt"))
				if err != nil {
					t.Fatalf("read first ANR snapshot: %v", err)
				}
				if !strings.Contains(string(firstSnapshot), "fixture_sample=1") {
					t.Fatalf("repeated ANR overwrote first before-recovery snapshot: %s", firstSnapshot)
				}
			}
		})
	}
}

func TestUnknownErrorInPostRecoverySampleIsNotDiscarded(t *testing.T) {
	got, calls, _, _ := runHelper(t, "post-recovery-unknown")
	assertOutcome(t, got, "failed", "unknown_active_error", 1)
	if countCallLog(calls, "shell am force-stop "+launcherPackage) != 1 {
		t.Fatalf("unexpected recovery calls:\n%s", calls)
	}
}

func TestOnlyVisibleCurrentLauncherANRTriggersRecovery(t *testing.T) {
	got, calls, _, _ := runHelper(t, "historical-anr")
	assertOutcome(t, got, "ready", "home_stable", 0)
	if strings.Contains(calls, "shell am force-stop "+launcherPackage) {
		t.Fatalf("hidden current window or historical lastanr triggered recovery:\n%s", calls)
	}
}

func TestLauncherANRWithOtherResolvedHomeDoesNotRecover(t *testing.T) {
	got, calls, _, _ := runHelper(t, "wrong-home-anr")
	assertOutcome(t, got, "failed", "unknown_active_error", 0)
	if strings.Contains(calls, "shell am force-stop "+launcherPackage) || strings.Contains(calls, "shell am start -W -a android.intent.action.MAIN") {
		t.Fatalf("launcher ANR recovery ran while HOME resolved to another app:\n%s", calls)
	}
}

func TestMalformedStateAndAdbTimeoutFailClosed(t *testing.T) {
	tests := []struct{ mode, reason string }{
		{"malformed", "state_unreadable"},
		{"malformed-home", "state_unreadable"},
		{"wrong-display", "state_unreadable"},
		{"malformed-window-id", "state_unreadable"},
		{"command-timeout", "state_unreadable"},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			got, calls, _, _ := runHelper(t, tc.mode)
			assertOutcome(t, got, "failed", tc.reason, 0)
			if strings.Contains(calls, "shell am force-stop "+launcherPackage) {
				t.Fatalf("unreadable state must not trigger recovery:\n%s", calls)
			}
			if tc.mode == "command-timeout" {
				if len(got.TimeoutValues) == 0 {
					t.Fatal("adb timeout wrapper was not called")
				}
				for _, budget := range got.TimeoutValues {
					if budget < 1 || budget > 5 {
						t.Fatalf("adb command timeout budget %d outside 1..5 seconds", budget)
					}
				}
			}
		})
	}
}

func TestNoReadyWindowFailsAtBoundedDeadline(t *testing.T) {
	for _, mode := range []string{"waiting", "mixed-window", "setup-permanent", "wrong-home"} {
		t.Run(mode, func(t *testing.T) {
			got, _, _, _ := runHelper(t, mode)
			assertOutcome(t, got, "failed", "timeout", 0)
			if got.ElapsedSeconds > 120 || got.StableSeconds != 0 {
				t.Fatalf("timeout exceeded configured bounds: %+v (expected <= 120s)", got)
			}
		})
	}
}

func TestHarnessFixturesAreNotProductionTimeoutOverrides(t *testing.T) {
	// The fixture advances only external `date` and `sleep`; the helper retains
	// its Bash SECONDS hard bound and fixed 120/6 second production constants.
	script, err := os.ReadFile(readinessScript(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"hard_deadline=$((SECONDS + 120))", "now - started >= 120", "stable_seconds >= 6"} {
		if !strings.Contains(string(script), required) {
			t.Fatalf("helper lost production readiness boundary %q", required)
		}
	}
	if strings.Contains(string(script), "READINESS_TEST") {
		t.Fatal("production helper contains a test bypass marker")
	}
}
