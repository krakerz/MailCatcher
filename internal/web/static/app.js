// Live inbox: on a new-message event for this inbox, re-fetch the rows.
(() => {
  const list = document.getElementById("messages");
  if (!list || list.dataset.live !== "1" || !window.EventSource) return;
  const inbox = list.dataset.inbox;
  const q = list.dataset.q;

  async function refresh() {
    const seen = new Set([...list.querySelectorAll("a.mail-link")].map((a) => a.getAttribute("href")));
    const picked = new Set([...list.querySelectorAll("[data-bulk-item]:checked")].map((c) => c.value));
    const res = await fetch(`/inbox/${inbox}/rows` + (q ? `?q=${encodeURIComponent(q)}` : ""), { credentials: "same-origin" });
    if (!res.ok) return;
    list.innerHTML = await res.text();
    for (const a of list.querySelectorAll("a.mail-link")) {
      if (!seen.has(a.getAttribute("href"))) a.closest(".mail-row").classList.add("new");
    }
    for (const c of list.querySelectorAll("[data-bulk-item]")) c.checked = picked.has(c.value);
    document.dispatchEvent(new Event("bulk-change"));
  }

  const es = new EventSource("/events");
  es.addEventListener("message", (ev) => {
    try {
      if (String(JSON.parse(ev.data).inbox) === inbox) refresh();
    } catch {}
  });
})();

// Theme switch (system / light / dark), remembered per browser; see theme.js.
(() => {
  const buttons = document.querySelectorAll("[data-theme-set]");
  if (!buttons.length) return;
  const read = () => {
    try {
      return localStorage.getItem("mc-theme") || "system";
    } catch {
      return "system";
    }
  };
  const apply = (t) => {
    if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
    else delete document.documentElement.dataset.theme;
    for (const b of buttons) b.setAttribute("aria-pressed", String(b.dataset.themeSet === t));
  };
  for (const b of buttons) {
    b.addEventListener("click", () => {
      try {
        localStorage.setItem("mc-theme", b.dataset.themeSet);
      } catch {}
      apply(b.dataset.themeSet);
    });
  }
  apply(read());
})();

// Mobile sidebar.
(() => {
  const shell = document.querySelector("[data-shell]");
  if (!shell) return;
  document.querySelector("[data-nav-toggle]")?.addEventListener("click", () => shell.classList.toggle("nav-open"));
  document.querySelector("[data-nav-close]")?.addEventListener("click", () => shell.classList.remove("nav-open"));
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
        addresses: val("textarea[name=addresses]"), name: val("[data-preview-name]"),
        priority: val("[data-preview-priority]"), id: box.dataset.inboxId || "0",
      });
      const res = await fetch("/admin/format-preview?" + q, { credentials: "same-origin" });
      if (!res.ok) return;
      const r = await res.json();
      out.replaceChildren();
      const line = (cls, text) => { const d = document.createElement("div"); d.className = cls; d.textContent = text; out.append(d); };
      if (r.error) line("bad", "✗ " + r.error);
      else line("ok", `${val("[data-preview-address]")} → ${r.address} → ${r.decoded}`);
      if (r.lands) line(r.conflict ? "bad" : "lands", (r.conflict ? "✗ " : "✓ ") + r.lands);
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

// Copy buttons (data-copy="text").
(() => {
  for (const b of document.querySelectorAll("[data-copy]")) {
    b.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(b.dataset.copy);
        const label = b.lastChild;
        const was = label.textContent;
        label.textContent = " Copied";
        setTimeout(() => (label.textContent = was), 1500);
      } catch {}
    });
  }
})();

// Rich editor: turns <textarea data-rich> into a basic formatting editor. The
// textarea keeps the plain-text version; a hidden "html" field carries the markup.
(() => {
  const sprite = (document.querySelector("svg.icon use")?.getAttribute("href") || "/static/icons.svg#").split("#")[0];
  const icon = (name) => `<svg class="icon" aria-hidden="true"><use href="${sprite}#${name}"></use></svg>`;
  const tools = [
    ["bold", "bold", "Bold"], ["italic", "italic", "Italic"], ["underline", "underline", "Underline"],
    ["strikeThrough", "strikethrough", "Strikethrough"], ["|"],
    ["insertUnorderedList", "list", "Bulleted list"], ["insertOrderedList", "list-ordered", "Numbered list"],
    ["quote", "text-quote", "Quote"], ["|"], ["link", "link", "Link"], ["image", "image-plus", "Inline image"], ["|"],
    ["removeFormat", "remove-formatting", "Clear formatting"],
  ];
  const MAX_IMAGE = 5 * 1024 * 1024;

  for (const ta of document.querySelectorAll("textarea[data-rich]")) {
    const form = ta.closest("form");
    const wrap = document.createElement("div");
    wrap.className = "editor";
    const bar = document.createElement("div");
    bar.className = "editor-toolbar";
    const area = document.createElement("div");
    area.className = "editor-area";
    area.contentEditable = "true";
    area.setAttribute("role", "textbox");
    area.setAttribute("aria-multiline", "true");
    const html = document.createElement("input");
    html.type = "hidden";
    html.name = "html";
    const picker = document.createElement("input");
    picker.type = "file";
    picker.accept = "image/png,image/jpeg,image/gif,image/webp";
    picker.hidden = true;

    if (ta.dataset.html) area.innerHTML = ta.dataset.html;
    else area.innerText = ta.value;

    for (const [cmd, ic, title] of tools) {
      if (cmd === "|") {
        bar.insertAdjacentHTML("beforeend", '<span class="sep"></span>');
        continue;
      }
      const b = document.createElement("button");
      b.type = "button";
      b.title = title;
      b.setAttribute("aria-label", title);
      b.innerHTML = icon(ic);
      b.addEventListener("mousedown", (e) => e.preventDefault()); // keep the selection
      b.addEventListener("click", () => run(cmd));
      bar.append(b);
    }

    function run(cmd) {
      area.focus();
      if (cmd === "quote") document.execCommand("formatBlock", false, "blockquote");
      else if (cmd === "link") {
        const url = prompt("Link address (https://…)");
        if (url && /^(https?:|mailto:)/i.test(url.trim())) document.execCommand("createLink", false, url.trim());
      } else if (cmd === "image") picker.click();
      else document.execCommand(cmd);
    }

    function insertImage(file) {
      if (!file || !file.type.startsWith("image/")) return;
      if (file.size > MAX_IMAGE) return alert("Images up to 5 MB, please.");
      const r = new FileReader();
      r.onload = () => {
        area.focus();
        document.execCommand("insertImage", false, r.result);
      };
      r.readAsDataURL(file);
    }
    picker.addEventListener("change", () => {
      insertImage(picker.files[0]);
      picker.value = "";
    });
    area.addEventListener("paste", (e) => {
      const files = [...(e.clipboardData?.files || [])].filter((f) => f.type.startsWith("image/"));
      if (files.length) {
        e.preventDefault();
        files.forEach(insertImage);
      }
    });
    area.addEventListener("drop", (e) => {
      const files = [...(e.dataTransfer?.files || [])].filter((f) => f.type.startsWith("image/"));
      if (files.length) {
        e.preventDefault();
        files.forEach(insertImage);
      }
    });

    form.addEventListener("submit", () => {
      html.value = area.innerHTML;
      ta.value = area.innerText;
    });
    document.execCommand("defaultParagraphSeparator", false, "p");
    ta.hidden = true;
    ta.required = false;
    wrap.append(bar, area);
    ta.after(wrap, html, picker);
  }
})();

// Bulk selection: select-all, count, enable buttons, highlight picked rows.
(() => {
  const bar = document.querySelector("[data-bulk]");
  if (!bar) return;
  const all = bar.querySelector("[data-bulk-all]");
  const count = bar.querySelector("[data-bulk-count]");
  const items = () => [...document.querySelectorAll("[data-bulk-item]")];
  function update() {
    const list = items();
    bar.hidden = list.length === 0;
    const n = list.filter((c) => c.checked).length;
    for (const c of list) c.closest(".mail-row, tr")?.classList.toggle("picked", c.checked);
    for (const b of bar.querySelectorAll("button")) b.disabled = n === 0;
    count.textContent = n ? `${n} selected` : "Select";
    all.checked = n > 0 && n === list.length;
    all.indeterminate = n > 0 && n < list.length;
  }
  all.addEventListener("change", () => {
    for (const c of items()) c.checked = all.checked;
    update();
  });
  document.addEventListener("change", (e) => {
    if (e.target.matches?.("[data-bulk-item]")) update();
  });
  document.addEventListener("bulk-change", update);
  update();
})();

// Confirm destructive buttons (data-confirm="question").
document.addEventListener("click", (e) => {
  const b = e.target.closest?.("button[data-confirm]");
  if (b && !confirm(b.dataset.confirm)) e.preventDefault();
});
