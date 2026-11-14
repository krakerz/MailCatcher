// Auto-forward rules as compiled by the app (internal/rules): every `all` regex
// must match the recipient and no `none` regex may. Kept free of Worker-only
// imports so the Go tests can run it under Node.
export function matchRules(rules, to) {
  to = String(to).toLowerCase();
  // Bounds regex work together with the wildcard cap on the app side.
  if (to.length > 254) return [];
  const test = (re) => {
    try {
      return new RegExp(re, "i").test(to);
    } catch {
      return false;
    }
  };
  const out = [];
  for (const r of rules || []) {
    if ((r.all || []).every(test) && !(r.none || []).some(test) && !out.includes(r.destination)) out.push(r.destination);
  }
  return out;
}

// Delivery may only move within the deployed INGEST_URL's parent domain, so a
// leaked HMAC secret cannot redirect mail elsewhere. Returns "" when allowed,
// else the error message. Without a usable pinned URL anything is allowed.
export function ingestHostAllowed(pinnedUrl, newUrl) {
  let pinned;
  try {
    pinned = new URL(pinnedUrl).hostname.toLowerCase();
  } catch {
    return "";
  }
  if (!pinned) return "";
  let host;
  try {
    host = new URL(newUrl).hostname.toLowerCase();
  } catch {
    return "ingest_url is not a valid URL";
  }
  if (host === pinned) return "";
  const labels = pinned.split(".");
  const parent = labels.slice(1).join(".");
  if (labels.length >= 3 && host.endsWith("." + parent)) return "";
  return `ingest_url host must be ${pinned}` + (labels.length >= 3 ? ` or another host under ${parent}` : "");
}
