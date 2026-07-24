// Minimal hash router: no server-side fallbacks needed, works from file cache offline.
export const route = $state({ path: window.location.hash.slice(1) || "/" });

window.addEventListener("hashchange", () => {
  route.path = window.location.hash.slice(1) || "/";
});
