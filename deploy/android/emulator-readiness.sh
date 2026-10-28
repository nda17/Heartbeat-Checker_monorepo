#!/usr/bin/env bash
# API 35 launcher readiness before instrumentation; never retries app tests.
set -euo pipefail
export LC_ALL=C
umask 077

[[ $# == 3 && -x $1 && $2 =~ ^[A-Za-z0-9._:-]+$ && -n $3 ]] || {
  echo 'Usage: bash emulator-readiness.sh ADB_BINARY SERIAL REPORT_DIRECTORY' >&2
  exit 2
}
adb_bin=$1
serial=$2
evidence="$3/readiness"
launcher=com.google.android.apps.nexuslauncher
home="$launcher/.NexusLauncherActivity"
[[ ! -e $evidence && ! -L $evidence ]] || { echo 'Readiness evidence directory already exists' >&2; exit 2; }
mkdir -p "$evidence/current"
started=$(date +%s)
hard_deadline=$((SECONDS + 120))
recovery_count=0
stable_since=
stable_seconds=0
stable_samples=0
after_recovery_pending=false
samples=0
finished=false

adb_command() {
  local remaining=$((hard_deadline - SECONDS - 1))
  (( remaining > 0 )) || return 124
  (( remaining <= 5 )) || remaining=5
  timeout --kill-after=1s "${remaining}s" "$adb_bin" -s "$serial" "$@"
}

finish() {
  local status=$1 reason=$2 now
  now=$(date +%s)
  if [[ $status == ready ]] && (( SECONDS >= hard_deadline || now - started >= 120 || now < started )); then
    status=failed
    reason=timeout
  fi
  jq -n --arg status "$status" --arg reason "$reason" --arg home "$home" \
    --argjson recoveries "$recovery_count" --argjson stable "$stable_seconds" \
    --argjson elapsed "$((now - started))" --argjson samples "$samples" \
    '{schemaVersion:1,status:$status,reason:$reason,homeComponent:$home,recoveryCount:$recoveries,stableSeconds:$stable,elapsedSeconds:$elapsed,samples:$samples,timeoutSeconds:120,requiredStableSeconds:6}' \
    > "$evidence/outcome.json"
  finished=true
  if [[ $status == ready ]]; then
    printf 'PASS emulator HOME readiness; recoveryCount=%s stableSeconds=%s\n' "$recovery_count" "$stable_seconds"
    exit 0
  fi
  printf 'FAIL emulator HOME readiness: %s; evidence=%s\n' "$reason" "$evidence" >&2
  exit 1
}
trap 'if [[ $finished != true ]]; then finish failed interrupted_or_command_error; fi' EXIT
trap 'finish failed interrupted' INT TERM HUP

collect_state() {
  adb_command shell cmd package resolve-activity --brief -a android.intent.action.MAIN -c android.intent.category.HOME \
    > "$evidence/current/home.txt" 2> "$evidence/current/home.stderr" || return 1
  local section service argument
  for section in power policy activities displays windows; do
    case $section in
      power) service=power; argument=;;
      policy|displays|windows) service=window; argument=$section;;
      activities) service=activity; argument=activities;;
    esac
    if [[ -n $argument ]]; then
      adb_command shell dumpsys "$service" "$argument" > "$evidence/current/$section.txt" 2> "$evidence/current/$section.stderr" || return 1
    else
      adb_command shell dumpsys "$service" > "$evidence/current/$section.txt" 2> "$evidence/current/$section.stderr" || return 1
    fi
  done
  samples=$((samples + 1))
}

save_snapshot() {
  local label=$1
  mkdir -p "$evidence/$label" || return 1
  cp "$evidence/current/"* "$evidence/$label/" || return 1
  adb_command shell dumpsys activity lastanr > "$evidence/$label/last-anr.txt" 2>&1 || return 1
  adb_command shell dumpsys input > "$evidence/$label/input.txt" 2>&1 || return 1
  adb_command logcat -d -t 2000 > "$evidence/$label/logcat.txt" 2>&1 || return 1
}

classify_state() {
  # Separate current dumps deliberately exclude the historical WINDOW MANAGER
  # LAST ANR section printed by an unqualified dumpsys window.
  awk -v short_home="$home" -v full_home="$launcher/$launcher.NexusLauncherActivity" '
    function field(key, value) { if (key in seen) bad=1; seen[key]++; values[key]=value }
    function component(line, value) {
      if (line ~ /=null$/ || line ~ /: null$/) return "null"
      value=line; sub(/^.* u0 /, "", value); sub(/[ }].*$/, "", value)
      if (value==short_home || value==full_home) return short_home
      if (value ~ /^[A-Za-z0-9_.]+\/[A-Za-z0-9_.]+$/) return value
      if (line ~ /Window\{[0-9a-f]+ u0 [^}]+\}/) return "other"
      bad=1; return "unreadable"
    }
    function window_end(visible, hidden, error) {
      if (!window_id) return
      visible=(view=="0x0" && surface=="true" && ready=="true" && drawn && on_screen=="true" && is_visible=="true")
      hidden=(on_screen=="false" && is_visible=="false")
      error=(title ~ /^Application Not Responding: / || title ~ /^Application Error: / || system_error)
      if (error) {
        if (!visible && !hidden) bad=1
        if (visible) {
          errors++
          if (title=="Application Not Responding: com.google.android.apps.nexuslauncher" && window_user=="u0" && window_display=="0" && owner_package=="android") launcher_anr++
        }
      }
      if (window_id==focus_id) {
        focused_windows++
        if (window_user!="u0" || window_display!="0") bad=1
        if (title!=short_home && title!=full_home) focused_home=0
        else if (visible) focused_home=1
        else if (!hidden) bad=1
      }
      window_id=""
    }
    { sub(/\r$/, "") }
    FILENAME ~ /\/home.txt$/ {
      if ($0 ~ /^[A-Za-z0-9_.]+\/[A-Za-z0-9_.]+$/) field("home", $0==full_home ? short_home : $0)
      else if ($0 !~ /^priority=/ && $0 !~ /^[[:space:]]*$/) bad=1
      next
    }
    FILENAME ~ /\/power.txt$/ {
      if ($0 ~ /^[[:space:]]*mWakefulness=/) { value=$0; sub(/^.*mWakefulness=/,"",value); field("wakefulness",value) }
      if ($0 ~ /^[[:space:]]*mWakefulnessChanging=/) { value=$0; sub(/^.*mWakefulnessChanging=/,"",value); field("changing",value) }
      next
    }
    FILENAME ~ /\/policy.txt$/ {
      if ($0 ~ /^[[:space:]]*KeyguardServiceDelegate:?$/) {
        policy++; in_policy=1; line=$0; sub(/[^[:space:]].*$/,"",line); policy_indent=length(line); next
      }
      if (in_policy && $0 !~ /^[[:space:]]*$/) {
        line=$0; sub(/[^[:space:]].*$/,"",line)
        if (length(line)<=policy_indent) in_policy=0
      }
      if (in_policy && $0 ~ /^[[:space:]]*(showing|dreaming|screenState|interactiveState)=/) {
        line=$0; sub(/^[[:space:]]*/,"",line); split(line,pair,"="); field(pair[1],pair[2])
      }
      next
    }
    FILENAME ~ /\/activities.txt$/ {
      if ($0 ~ /^[[:space:]]*ResumedActivity:/) field("resumed",component($0))
      next
    }
    FILENAME ~ /\/displays.txt$/ {
      if ($0 ~ /^[[:space:]]*Display: mDisplayId=/) {
        in_display=($0 ~ /^[[:space:]]*Display: mDisplayId=0([[:space:]]|$)/)
        if (in_display) displays++
      }
      if (in_display && $0 ~ /^[[:space:]]*mCurrentFocus=/) {
        if ($0 !~ /^[[:space:]]*mCurrentFocus=(null|Window\{[0-9a-f]+ u0 [^{}]+\})$/) bad=1
        field("focus",component($0)); focus_id=$0
        if (values["focus"]=="null") focus_id="null"
        else { sub(/^.*Window\{/,"",focus_id); sub(/ .*/,"",focus_id) }
      }
      if (in_display && $0 ~ /^[[:space:]]*mFocusedApp=/) field("focused_app",component($0))
      next
    }
    FILENAME ~ /\/windows.txt$/ {
      if ($0 ~ /^[[:space:]]*Window #[0-9]+ Window\{.*\}:$/) {
        window_end(); windows++; line=$0; sub(/^.*Window\{/,"",line); sub(/\}:$/,"",line)
        window_id=line; sub(/ .*/,"",window_id); title=line; sub(/^[^ ]+ u[0-9]+ /,"",title)
        if (window_id !~ /^[0-9a-f]+$/) bad=1
        window_user=line; sub(/^[^ ]+ /,"",window_user); sub(/ .*/,"",window_user)
        window_display=owner_package=""
        view=surface=ready=on_screen=is_visible=""; drawn=system_error=0
      }
      if ($0 ~ /^[[:space:]]*mDisplayId=/) { window_display=$0; sub(/^.*mDisplayId=/,"",window_display); sub(/ .*/,"",window_display) }
      if ($0 ~ /^[[:space:]]*mOwnerUid=.* package=/) { owner_package=$0; sub(/^.* package=/,"",owner_package); sub(/ .*/,"",owner_package) }
      if ($0 ~ /mViewVisibility=/) { view=$0; sub(/^.*mViewVisibility=/,"",view); sub(/ .*/,"",view) }
      if ($0 ~ /^[[:space:]]*mHasSurface=/) {
        surface=$0; sub(/^.*mHasSurface=/,"",surface); sub(/ .*/,"",surface)
        ready=$0; sub(/^.*isReadyForDisplay\(\)=/,"",ready); sub(/ .*/,"",ready)
      }
      if ($0 ~ /^[[:space:]]*Surface: shown=true[[:space:]]+mDrawState=HAS_DRAWN/) drawn=1
      if ($0 ~ /^[[:space:]]*isOnScreen=/) { on_screen=$0; sub(/^.*=/,"",on_screen) }
      if ($0 ~ /^[[:space:]]*isVisible=/) { is_visible=$0; sub(/^.*=/,"",is_visible) }
      if ($0 ~ /(^|[[:space:]=])SYSTEM_ERROR([[:space:]]|$)/) system_error=1
    }
    END {
      window_end()
      required="home wakefulness changing showing dreaming screenState interactiveState resumed focus focused_app"
      count=split(required,keys," "); for(i=1;i<=count;i++) if(seen[keys[i]]!=1) bad=1
      if (policy!=1 || displays!=1 || windows<1 || values["home"]!=short_home) bad=1
      if (values["changing"] !~ /^(true|false)$/ || values["showing"] !~ /^(true|false)$/ || values["dreaming"] !~ /^(true|false)$/) bad=1
      if (values["wakefulness"] !~ /^(Awake|Asleep|Dreaming|Dozing)$/) bad=1
      if (values["focus"]!="null" && focused_windows!=1) bad=1
      if (bad) print "state_unreadable"
      else if (errors>1 || (errors==1 && launcher_anr!=1)) print "unknown_active_error"
      else if (launcher_anr==1) print "launcher_anr"
      else if (values["wakefulness"]=="Awake" && values["changing"]=="false" && values["showing"]=="false" && values["dreaming"]=="false" && values["screenState"]=="SCREEN_STATE_ON" && values["interactiveState"]=="INTERACTIVE_STATE_AWAKE" && values["resumed"]==short_home && values["focus"]==short_home && values["focused_app"]==short_home && focused_home) print "ready"
      else print "waiting"
    }
  ' "$evidence/current/home.txt" "$evidence/current/power.txt" "$evidence/current/policy.txt" \
    "$evidence/current/activities.txt" "$evidence/current/displays.txt" "$evidence/current/windows.txt"
}

while true; do
  now=$(date +%s)
  if (( SECONDS >= hard_deadline || now - started >= 120 || now < started )); then
    save_snapshot final || true
    finish failed timeout
  fi
  collect_state || finish failed state_unreadable
  state=$(classify_state) || finish failed state_unreadable
  if (( samples == 1 )); then save_snapshot initial || finish failed diagnostics_unreadable; fi
  if [[ $after_recovery_pending == true ]]; then
    save_snapshot after-recovery || finish failed diagnostics_unreadable
    after_recovery_pending=false
  fi
  now=$(date +%s)
  (( SECONDS < hard_deadline && now - started < 120 && now >= started )) || finish failed timeout
  printf '%s\t%s\t%s\n' "$now" "$state" "$recovery_count" >> "$evidence/samples.tsv"
  case $state in
    launcher_anr)
      if (( recovery_count != 0 )); then
        save_snapshot final || true
        finish failed repeated_launcher_anr
      fi
      save_snapshot before-recovery || finish failed diagnostics_unreadable
      recovery_count=1
      adb_command shell am force-stop "$launcher" > "$evidence/recovery-force-stop.txt" 2>&1 || finish failed recovery_failed
      adb_command shell am start -W -a android.intent.action.MAIN -c android.intent.category.HOME -n "$home" \
        > "$evidence/recovery-home-start.txt" 2>&1 || finish failed recovery_failed
      stable_since=
      stable_seconds=0
      stable_samples=0
      after_recovery_pending=true
      ;;
    ready)
      [[ -n $stable_since ]] || stable_since=$now
      stable_samples=$((stable_samples + 1))
      stable_seconds=$((now - stable_since))
      if (( stable_samples >= 2 && stable_seconds >= 6 )); then
        save_snapshot final || finish failed diagnostics_unreadable
        finish ready home_stable
      fi
      ;;
    waiting) stable_since=; stable_seconds=0; stable_samples=0;;
    state_unreadable|unknown_active_error)
      save_snapshot final || true
      finish failed "$state"
      ;;
    *) finish failed state_unreadable;;
  esac
  sleep 1
done
