// Paints the saved or system theme before first paint. It is a file, not an inline script in
// index.html, because the server's Content-Security-Policy is script-src 'self': an inline
// script is blocked there, and the sign-in screens (which never mount the header that
// re-applies the theme) stayed light. Keep the key and the rules in step with src/lib/theme.ts.
try {
  var stored = localStorage.getItem("podium-relay-theme");
  var theme =
    stored === "dark" || stored === "light"
      ? stored
      : window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
  document.documentElement.dataset.theme = theme;
  var themeColor = document.querySelector('meta[name="theme-color"]');
  if (themeColor) themeColor.setAttribute("content", theme === "dark" ? "#060708" : "#f2f4f6");
} catch {
  document.documentElement.dataset.theme = "light";
}
