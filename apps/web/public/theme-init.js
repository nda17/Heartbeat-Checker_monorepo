try {
  document.documentElement.dataset.theme =
    localStorage.getItem("heartbeat-theme") === "dark" ? "dark" : "light";
} catch {
  document.documentElement.dataset.theme = "light";
}
