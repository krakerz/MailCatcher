// Applies the saved theme before first paint (loaded in <head>, not deferred).
// "light" / "dark" are explicit choices; anything else follows the OS.
(() => {
  let t = null;
  try {
    t = localStorage.getItem("mc-theme");
  } catch {}
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
})();
