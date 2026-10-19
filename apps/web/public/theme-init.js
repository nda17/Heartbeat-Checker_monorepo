try {
  document.documentElement.dataset.theme =
    localStorage.getItem("heartbeat-theme") === "dark" ? "dark" : "light";
} catch {
  document.documentElement.dataset.theme = "light";
}

// This tab-local hint only hides installation offers; it is not an auth signal.
const standalone = window.matchMedia("(display-mode: standalone)");
let installed =
  standalone.matches ||
  /^android-app:\/\/ru\.heartbeatchecker\.app\/?$/.test(document.referrer);
try {
  installed ||= sessionStorage.getItem("heartbeat-installed-app") === "1";
} catch {
  // Live signals remain usable when tab storage is unavailable.
}
function updateInstalledApp() {
  try {
    if (installed) sessionStorage.setItem("heartbeat-installed-app", "1");
    else sessionStorage.removeItem("heartbeat-installed-app");
  } catch {
    // Updating this page does not depend on persisting the hint.
  }
  document.documentElement.dataset.installedApp = String(installed);
}
updateInstalledApp();
standalone.addEventListener("change", () => {
  installed = standalone.matches;
  updateInstalledApp();
});
