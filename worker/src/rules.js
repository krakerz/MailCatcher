// Auto-forward rules as compiled by the app (internal/rules): every `all` regex
// must match the recipient and no `none` regex may. Kept free of Worker-only
// imports so the Go tests can run it under Node.
export function matchRules(rules, to) {
  to = String(to).toLowerCase();
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
