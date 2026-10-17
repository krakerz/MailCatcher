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
  const MAX_DIM = 1600; // wider images are redrawn at this width before they are embedded
  const FIT = 600; // wider images are shown at this width until resized
  const MIN_W = 40;
  const sizes = [["Small", 240], ["Medium", 480], ["Large", 720], ["Original", 0]];

  // Redraws a big png/jpeg/webp data URL at MAX_DIM wide; any failure keeps the original.
  function shrink(url, type) {
    if (!/^image\/(png|jpeg|webp)$/.test(type)) return Promise.resolve(url);
    return new Promise((resolve) => {
      const im = new Image();
      im.onerror = () => resolve(url);
      im.onload = () => {
        try {
          if (im.naturalWidth <= MAX_DIM) return resolve(url);
          const c = document.createElement("canvas");
          c.width = MAX_DIM;
          c.height = Math.max(1, Math.round((im.naturalHeight * MAX_DIM) / im.naturalWidth));
          c.getContext("2d").drawImage(im, 0, 0, c.width, c.height);
          const out = c.toDataURL(type, 0.9);
          resolve(out.startsWith("data:" + type) && out.length < url.length ? out : url);
        } catch {
          resolve(url);
        }
      };
      im.src = url;
    });
  }

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
      r.onload = async () => {
        const url = await shrink(r.result, file.type);
        area.focus();
        document.execCommand("insertImage", false, url);
        const img = [...area.querySelectorAll("img")].find((i) => i.getAttribute("src") === url && !i.hasAttribute("width"));
        if (img) fit(img);
      };
      r.readAsDataURL(file);
    }
    // Shows a freshly inserted image at FIT wide when it is bigger than that.
    function fit(img) {
      const f = () => {
        if (img.naturalWidth > FIT) img.setAttribute("width", FIT);
        place();
      };
      if (img.complete && img.naturalWidth) f();
      else img.addEventListener("load", f, { once: true });
    }

    // Image selection: a floating size toolbar and a corner drag handle, both
    // outside the editable area so they never end up in the mail.
    const sizeBar = document.createElement("div");
    sizeBar.className = "img-tools";
    sizeBar.setAttribute("role", "toolbar");
    sizeBar.setAttribute("aria-label", "Image size");
    sizeBar.hidden = true;
    const handle = document.createElement("div");
    handle.className = "img-handle";
    handle.hidden = true;
    let sel = null;
    let resizeObs = null;

    const maxWidth = (img) => {
      const cs = getComputedStyle(area);
      const room = area.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
      return Math.max(MIN_W, Math.min(img.naturalWidth || Infinity, room));
    };
    const setWidth = (img, w) => {
      if (w) img.setAttribute("width", Math.round(w));
      else img.removeAttribute("width");
      place();
    };

    for (const [label, px] of sizes) {
      const b = document.createElement("button");
      b.type = "button";
      b.textContent = label;
      b.dataset.size = px;
      b.addEventListener("click", () => sel && setWidth(sel, px && Math.min(px, sel.naturalWidth || px)));
      sizeBar.append(b);
    }
    const del = document.createElement("button");
    del.type = "button";
    del.className = "danger";
    del.textContent = "Remove";
    del.addEventListener("click", removeSelected);
    sizeBar.append(del);
    sizeBar.addEventListener("mousedown", (e) => e.preventDefault()); // keep the caret

    function removeSelected() {
      if (!sel) return;
      sel.remove();
      deselect();
      area.focus();
    }

    // Moves the toolbar and handle to the selected image, hiding them when it is scrolled out of view.
    function place() {
      if (!sel) return;
      if (!area.contains(sel)) return deselect();
      const r = sel.getBoundingClientRect();
      const a = area.getBoundingClientRect();
      const w = wrap.getBoundingClientRect();
      const ox = w.left + wrap.clientLeft;
      const oy = w.top + wrap.clientTop;
      const shown = r.bottom > a.top && r.top < a.bottom;
      sizeBar.hidden = handle.hidden = !shown;
      if (!shown) return;
      const cur = parseInt(sel.getAttribute("width"), 10) || 0;
      for (const b of sizeBar.querySelectorAll("[data-size]")) {
        const px = +b.dataset.size;
        const want = px ? Math.min(px, sel.naturalWidth || px) : 0;
        b.classList.toggle("active", want === cur);
      }
      const th = sizeBar.offsetHeight;
      let top = r.top - oy - th - 6;
      if (top < a.top - oy) top = r.bottom + 6 + th <= a.bottom ? r.bottom - oy + 6 : Math.max(r.top, a.top) - oy + 6;
      sizeBar.style.top = top + "px";
      sizeBar.style.left = Math.max(4, Math.min(r.left - ox, wrap.clientWidth - sizeBar.offsetWidth - 4)) + "px";
      const inside = r.bottom <= a.bottom && r.right <= a.right;
      handle.hidden = !inside;
      handle.style.left = r.right - ox - 9 + "px";
      handle.style.top = r.bottom - oy - 9 + "px";
    }

    function select(img) {
      if (sel === img) return place();
      deselect();
      sel = img;
      img.classList.add("sel");
      resizeObs = new ResizeObserver(place);
      resizeObs.observe(img);
      place();
    }

    function deselect() {
      if (resizeObs) resizeObs.disconnect();
      resizeObs = null;
      if (sel) {
        sel.classList.remove("sel");
        if (!sel.getAttribute("class")) sel.removeAttribute("class");
      }
      sel = null;
      sizeBar.hidden = handle.hidden = true;
    }

    let drag = null;
    handle.addEventListener("pointerdown", (e) => {
      if (!sel) return;
      e.preventDefault();
      handle.setPointerCapture(e.pointerId);
      drag = { x: e.clientX, w: sel.getBoundingClientRect().width };
    });
    handle.addEventListener("pointermove", (e) => {
      if (!drag || !sel) return;
      setWidth(sel, Math.max(MIN_W, Math.min(drag.w + e.clientX - drag.x, maxWidth(sel))));
    });
    for (const ev of ["pointerup", "pointercancel"]) handle.addEventListener(ev, () => (drag = null));

    area.addEventListener("click", (e) => {
      if (e.target instanceof HTMLImageElement) select(e.target);
      else deselect();
    });
    area.addEventListener("keydown", (e) => {
      if (!sel) return;
      if (e.key === "Delete" || e.key === "Backspace") {
        e.preventDefault();
        removeSelected();
      } else if (e.key === "Escape" || e.key === "Enter" || (e.key.length === 1 && !e.ctrlKey && !e.metaKey)) deselect();
    });
    document.addEventListener("pointerdown", (e) => {
      if (sel && e.target !== sel && !sizeBar.contains(e.target) && e.target !== handle) deselect();
    });
    area.addEventListener("load", place, true); // images finishing to load shift the layout
    area.addEventListener("scroll", place);
    window.addEventListener("resize", place);
    new MutationObserver(() => sel && !area.contains(sel) && deselect()).observe(area, { childList: true, subtree: true });

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
      deselect(); // drops the selection class before the markup is read
      html.value = area.innerHTML;
      ta.value = area.innerText;
    });
    document.execCommand("defaultParagraphSeparator", false, "p");
    document.execCommand("enableObjectResizing", false, false); // Firefox's own handles would fight ours
    ta.hidden = true;
    ta.required = false;
    wrap.append(bar, area, sizeBar, handle);
    ta.after(wrap, html, picker);
  }
})();

// Bulk selection: select-all, count, swap idle/selection actions, highlight picked rows.
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
    for (const g of bar.querySelectorAll("[data-bulk-actions]")) g.hidden = n === 0;
    for (const g of bar.querySelectorAll("[data-bulk-idle]")) g.hidden = n > 0;
    count.textContent = n ? `${n} selected` : `${list.length} message${list.length === 1 ? "" : "s"}`;
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

// Selects that submit their form on change (data-autosubmit). data-confirm-change="question"
// asks first and puts the previous value back when declined.
(() => {
  for (const sel of document.querySelectorAll("select[data-autosubmit]")) {
    let prev = sel.value;
    sel.addEventListener("change", () => {
      if (sel.dataset.confirmChange && !confirm(sel.dataset.confirmChange)) {
        sel.value = prev;
        return;
      }
      prev = sel.value;
      sel.form.submit();
    });
  }
})();

// Ctrl/Cmd+Enter sends a reply, compose or forward form (data-send).
document.addEventListener("keydown", (e) => {
  if (e.key !== "Enter" || !(e.ctrlKey || e.metaKey) || e.repeat) return;
  const f = e.target.closest?.("form[data-send]");
  if (!f) return;
  e.preventDefault();
  f.requestSubmit();
});

// Confirm destructive buttons (data-confirm="question").
document.addEventListener("click", (e) => {
  const b = e.target.closest?.("button[data-confirm]");
  if (b && !confirm(b.dataset.confirm)) e.preventDefault();
});
