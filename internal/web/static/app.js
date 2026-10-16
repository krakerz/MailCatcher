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
