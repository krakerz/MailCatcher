// Live inbox: on a new-message event for this inbox, re-fetch the table rows.
(() => {
  const table = document.getElementById("messages");
  if (!table || table.dataset.live !== "1" || !window.EventSource) return;
  const inbox = table.dataset.inbox;
  const q = table.dataset.q;
  const tbody = table.tBodies[0];

  async function refresh() {
    const seen = new Set([...tbody.querySelectorAll("a[href^='/m/']")].map((a) => a.getAttribute("href")));
    const res = await fetch(`/inbox/${inbox}/rows` + (q ? `?q=${encodeURIComponent(q)}` : ""), { credentials: "same-origin" });
    if (!res.ok) return;
    tbody.innerHTML = await res.text();
    for (const tr of tbody.rows) {
      const a = tr.querySelector("a[href^='/m/']");
      if (a && !seen.has(a.getAttribute("href"))) tr.classList.add("new");
    }
  }

  const es = new EventSource("/events");
  es.addEventListener("message", (ev) => {
    try {
      if (String(JSON.parse(ev.data).inbox) === inbox) refresh();
    } catch {}
  });
})();

// Address format preview on the inbox forms.
(() => {
  for (const box of document.querySelectorAll("[data-preview]")) {
    const form = box.closest("form");
    const val = (sel) => (form.querySelector(sel) || {}).value || "";
    const out = box.querySelector("[data-preview-out]");
    let timer;
    async function update() {
      const q = new URLSearchParams({
        format: val("[data-preview-format]"), tld: val("[data-preview-tld]"), slug: val("[data-preview-slug]"),
        domain: val("[data-preview-domain]"), address: val("[data-preview-address]"),
      });
      const res = await fetch("/admin/format-preview?" + q, { credentials: "same-origin" });
      if (!res.ok) return;
      const r = await res.json();
      out.replaceChildren();
      const line = (cls, text) => { const d = document.createElement("div"); d.className = cls; d.textContent = text; out.append(d); };
      if (r.error) line("bad", "✗ " + r.error);
      else line("ok", `${val("[data-preview-address]")} → ${r.address} → ${r.decoded}`);
      for (const w of r.warnings) line("warn", "⚠ " + w);
    }
    // Assumed TLD only matters for {name} without {tld}.
    function toggleTLD() {
      const f = val("[data-preview-format]");
      const needed = f.includes("{name}") && !f.includes("{tld}");
      for (const el of form.querySelectorAll("[data-tld-field]")) el.hidden = !needed;
      for (const el of form.querySelectorAll("[data-tld-echo]")) el.textContent = val("[data-preview-tld]") || "?";
    }
    form.addEventListener("input", () => { toggleTLD(); clearTimeout(timer); timer = setTimeout(update, 250); });
    toggleTLD();
    update();
  }
})();

// Compose: show which catch-all address the identity sends from.
(() => {
  const form = document.querySelector("[data-compose]");
  if (!form) return;
  const inbox = form.querySelector("[data-compose-inbox]");
  const ident = form.querySelector("[data-compose-identity]");
  const out = form.querySelector("[data-compose-from]");
  let timer;
  async function update() {
    if (!ident.value.trim()) { out.textContent = ""; return; }
    const q = new URLSearchParams({ inbox: inbox.value, identity: ident.value });
    const res = await fetch("/compose/from?" + q, { credentials: "same-origin" });
    if (!res.ok) return;
    const r = await res.json();
    out.textContent = r.error ? "✗ " + r.error : "Sends from " + r.from;
  }
  for (const el of [inbox, ident]) el.addEventListener("input", () => { clearTimeout(timer); timer = setTimeout(update, 250); });
  update();
})();

// Inline "send verification link" after a send to an unverified address.
(() => {
  for (const box of document.querySelectorAll("[data-add-destination]")) {
    const btn = box.querySelector("button");
    const out = box.querySelector("[data-add-destination-out]");
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      const body = new URLSearchParams({ csrf: document.body.dataset.csrf, email: box.dataset.addDestination });
      const res = await fetch("/destinations", { method: "POST", body, credentials: "same-origin", headers: { "X-Requested-With": "fetch" } });
      const r = await res.json().catch(() => ({ error: "request failed" }));
      out.textContent = r.error || r.notice;
      if (r.error) btn.disabled = false;
    });
  }
})();
