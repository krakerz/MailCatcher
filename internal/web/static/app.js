// The page as the server sent it, before any setup below changes it; kept for Back.
const initialHTML = document.documentElement.outerHTML;

// Per-page setup. Runs on load and again after every in-page navigation (see nav
// below); listeners on document/window must pass the signal so they end with the page.
const pageSetups = [];
const onPage = (fn) => pageSetups.push(fn);
let pageAbort = new AbortController();
function setupPage() {
  pageAbort.abort();
  pageAbort = new AbortController();
  for (const fn of pageSetups) {
    try {
      fn(pageAbort.signal);
    } catch (err) {
      console.error(err);
    }
  }
}

// Live inbox: one event stream per tab, kept across navigations; a new-message
// event for the open inbox re-fetches its rows.
async function refreshRows(list) {
  const seen = new Set([...list.querySelectorAll("a.mail-link")].map((a) => a.getAttribute("href")));
  const picked = new Set([...list.querySelectorAll("[data-bulk-item]:checked")].map((c) => c.value));
  const q = list.dataset.q;
  const res = await fetch(`/inbox/${list.dataset.inbox}/rows` + (q ? `?q=${encodeURIComponent(q)}` : ""), { credentials: "same-origin" });
  if (!res.ok || !list.isConnected) return;
  list.innerHTML = await res.text();
  for (const a of list.querySelectorAll("a.mail-link")) {
    if (!seen.has(a.getAttribute("href"))) a.closest(".mail-row").classList.add("new");
  }
  for (const c of list.querySelectorAll("[data-bulk-item]")) c.checked = picked.has(c.value);
  document.dispatchEvent(new Event("bulk-change"));
}

const live = (() => {
  let es = null;
  function open() {
    if (es || !window.EventSource) return;
    es = new EventSource("/events");
    es.addEventListener("message", (ev) => {
      try {
        const list = document.getElementById("messages");
        if (list?.dataset.live === "1" && String(JSON.parse(ev.data).inbox) === list.dataset.inbox) refreshRows(list);
      } catch {}
    });
  }
  // A closed stream lets the browser keep the page for Back (bfcache).
  addEventListener("pagehide", () => {
    es?.close();
    es = null;
  });
  return { open };
})();

onPage(() => {
  if (document.getElementById("messages")?.dataset.live === "1") live.open();
});

// Theme switch (system / light / dark), remembered per browser; see theme.js.
onPage((signal) => {
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
});

// Mobile sidebar.
onPage((signal) => {
  const shell = document.querySelector("[data-shell]");
  if (!shell) return;
  document.querySelector("[data-nav-toggle]")?.addEventListener("click", () => shell.classList.toggle("nav-open"));
  document.querySelector("[data-nav-close]")?.addEventListener("click", () => shell.classList.remove("nav-open"));
});

// Address format preview on the inbox forms.
onPage((signal) => {
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
});

// Compose: show which catch-all address the identity sends from.
onPage((signal) => {
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
});

// Inline "send verification link" after a send to an unverified address.
onPage((signal) => {
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
});

// Copy buttons (data-copy="text").
onPage((signal) => {
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
});

// Rich editor: turns <textarea data-rich> into a basic formatting editor. The
// textarea keeps the plain-text version; a hidden "html" field carries the markup.
onPage((signal) => {
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
    }, { signal });
    area.addEventListener("load", place, true); // images finishing to load shift the layout
    area.addEventListener("scroll", place);
    window.addEventListener("resize", place, { signal });
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
});

// Bulk selection: select-all, count, swap idle/selection actions, highlight picked rows.
onPage((signal) => {
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
  }, { signal });
  document.addEventListener("bulk-change", update, { signal });
  update();
});

// Selects that submit their form on change (data-autosubmit). data-confirm-change="question"
// asks first and puts the previous value back when declined.
onPage((signal) => {
  for (const sel of document.querySelectorAll("select[data-autosubmit]")) {
    let prev = sel.value;
    sel.addEventListener("change", () => {
      if (sel.dataset.confirmChange && !confirm(sel.dataset.confirmChange)) {
        sel.value = prev;
        return;
      }
      prev = sel.value;
      sel.form.requestSubmit(); // through nav, like a button press
    });
  }
});

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

// In-page navigation: same-origin links and forms load with fetch and swap <body>,
// so CSS, JS and the event stream stay loaded. Back/Forward show the kept copy of a
// page at once and then refresh its live parts ([data-fresh]). Without JS (or
// data-no-nav) everything falls back to normal page loads.
const nav = (() => {
  const KEEP = 20; // pages kept for Back/Forward, in memory only
  const PREFETCH_TTL = 10000;
  const pages = new Map(); // history key → { html, y }
  const prefetched = new Map(); // url → { at, load }
  const stylesheet = (doc) => doc.querySelector('link[rel="stylesheet"]')?.getAttribute("href");
  const assets = stylesheet(document);
  let current = history.state?.nav || newKey();
  let inflight = null;

  function newKey() {
    return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
  }
  function keep(key, html) {
    pages.delete(key);
    pages.set(key, { html, y: 0 });
    while (pages.size > KEEP) pages.delete(pages.keys().next().value);
  }
  function rememberScroll() {
    const p = pages.get(current);
    if (p) p.y = scrollY;
  }

  // Attachments, raw source and the message body frame stay real downloads/loads.
  function handled(url) {
    return url.origin === location.origin && !/^\/(static|api|events)(\/|$)/.test(url.pathname) &&
      !/^\/m\/\d+\/(att|raw|body)(\/|$)/.test(url.pathname) && url.pathname !== "/logout";
  }

  async function load(url, init = {}) {
    const res = await fetch(url, { credentials: "same-origin", ...init });
    const type = res.headers.get("Content-Type") || "";
    return { url: res.url, status: res.status, redirected: res.redirected, html: type.startsWith("text/html"), text: await res.text() };
  }

  function takePrefetch(url) {
    const p = prefetched.get(url);
    prefetched.delete(url);
    return p && Date.now() - p.at < PREFETCH_TTL ? p.load : null;
  }

  function render(doc) {
    document.title = doc.title;
    document.body.replaceWith(document.adoptNode(doc.body));
    setupPage();
    document.querySelector("[autofocus]")?.focus();
  }

  function scrollToPos(y, hash) {
    const target = hash && document.getElementById(decodeURIComponent(hash.slice(1)));
    if (target) target.scrollIntoView();
    else window.scrollTo(0, y);
  }

  function busy(on) {
    clearTimeout(busy.timer);
    if (on) busy.timer = setTimeout(() => document.documentElement.classList.add("nav-busy"), 120);
    else document.documentElement.classList.remove("nav-busy");
    if (on) document.documentElement.setAttribute("aria-busy", "true");
    else document.documentElement.removeAttribute("aria-busy");
  }

  // visit loads url and shows it. opts: method/body for form posts, restore = history
  // key being returned to (no new history entry), form = the submitting form.
  async function visit(url, { method = "GET", body = null, restore = null, form = null } = {}) {
    inflight?.abort();
    const ctrl = (inflight = new AbortController());
    if (method !== "GET") prefetched.clear();
    busy(true);
    let r;
    try {
      r = await ((method === "GET" && takePrefetch(url)) || load(url, { method, body, signal: ctrl.signal }));
    } catch (err) {
      if (ctrl.signal.aborted) return;
      busy(false);
      if (method === "GET") location.assign(url); // let the browser show what went wrong
      else {
        form?.querySelectorAll("button, input[type=submit]").forEach((b) => (b.disabled = false));
        alert("Could not reach mailcatcher. Nothing was sent; try again.");
      }
      return;
    }
    if (ctrl.signal.aborted) return;
    inflight = null;
    busy(false);
    if (!r.html && r.status === 401) return location.assign("/login?next=" + encodeURIComponent(location.pathname + location.search));
    if (!r.html) {
      if (method === "GET") return location.assign(r.url);
      r.html = true; // a plain-text error from a post: show it like the browser would
      const pre = document.createElement("pre");
      pre.textContent = r.text;
      r.text = `<!doctype html><title>${r.status}</title><body><main class="content">${pre.outerHTML}</main></body>`;
    }
    const doc = new DOMParser().parseFromString(r.text, "text/html");
    if (doc.querySelector('link[rel="stylesheet"]') && stylesheet(doc) !== assets) return location.assign(r.url); // new version deployed
    if (method !== "GET") pages.clear(); // anything kept may be out of date now

    rememberScroll();
    const hash = method === "GET" ? new URL(url).hash : "";
    const moved = method === "GET" || r.redirected; // the answer is a page of its own
    if (restore) {
      current = restore;
    } else if (moved && r.url !== location.href.split("#")[0]) {
      current = newKey();
      history.pushState({ nav: current }, "", r.url + hash);
    } else {
      // Same page again, or a form re-shown with an error: no new history entry.
      history.replaceState({ nav: current }, "", moved ? r.url + hash : location.href);
    }
    keep(current, r.text);
    render(doc);
    scrollToPos(0, hash);
  }

  // Shows a kept page at once, then swaps in fresh [data-fresh] parts (unread counts, mail list).
  async function revalidate(key) {
    let r;
    try {
      r = await load(location.href);
    } catch {
      return;
    }
    if (key !== current || !r.html || r.redirected) return;
    const doc = new DOMParser().parseFromString(r.text, "text/html");
    for (const el of document.querySelectorAll("[data-fresh][id]")) {
      const fresh = doc.getElementById(el.id);
      if (fresh) el.replaceWith(document.adoptNode(fresh));
    }
    document.dispatchEvent(new Event("bulk-change"));
    const p = pages.get(key);
    if (p) p.html = r.text;
  }

  addEventListener("popstate", (e) => {
    const key = e.state?.nav;
    if (!key || key === current) return; // an in-page #anchor
    inflight?.abort();
    busy(false);
    rememberScroll();
    const p = pages.get(key);
    if (!p) return visit(location.href, { restore: key });
    current = key;
    render(new DOMParser().parseFromString(p.html, "text/html"));
    scrollToPos(p.y);
    revalidate(key);
  });

  // Restored from the browser's own Back cache (left the app and came back).
  addEventListener("pageshow", (e) => {
    if (!e.persisted) return;
    if (document.getElementById("messages")?.dataset.live === "1") live.open();
    revalidate(current);
  });

  document.addEventListener("click", (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = e.target.closest?.("a[href]");
    if (!a || a.target || a.hasAttribute("download") || a.closest("[data-no-nav]")) return;
    const url = new URL(a.href, location.href);
    if (!handled(url)) return;
    if (url.hash && url.pathname === location.pathname && url.search === location.search) return;
    e.preventDefault();
    visit(url.href);
  });

  // Browsers that cannot put the clicked button into FormData submit natively
  // (buttons carry values like action=disable).
  const submitterOK = (() => {
    try {
      const f = document.createElement("form");
      const b = document.createElement("button");
      b.name = "x";
      f.append(b);
      return new FormData(f, b).has("x");
    } catch {
      return false;
    }
  })();

  document.addEventListener("submit", (e) => {
    const form = e.target;
    if (e.defaultPrevented || !submitterOK || form.closest("[data-no-nav]")) return;
    const sub = e.submitter;
    // getAttribute, not form.action/method: inputs named "action" shadow those properties.
    const method = (sub?.getAttribute("formmethod") || form.getAttribute("method") || "get").toLowerCase();
    const url = new URL(sub?.getAttribute("formaction") || form.getAttribute("action") || location.href, location.href);
    if (form.getAttribute("target") || (method !== "get" && method !== "post") || !handled(url)) return;
    const data = new FormData(form, sub);
    e.preventDefault();
    if (method === "get") {
      url.search = new URLSearchParams(data).toString();
      url.hash = "";
      return visit(url.href);
    }
    const multipart = (sub?.getAttribute("formenctype") || form.getAttribute("enctype")) === "multipart/form-data";
    form.querySelectorAll("button, input[type=submit]").forEach((b) => (b.disabled = true)); // no double sends
    visit(url.href, { method: "POST", body: multipart ? data : new URLSearchParams(data), form });
  });

  // Hovering a link for a moment starts loading it. Not message links: opening a
  // message marks it read.
  let hoverTimer;
  function prefetch(a) {
    if (a.target || a.hasAttribute("download") || a.closest("[data-no-nav]")) return;
    const url = new URL(a.href, location.href);
    url.hash = "";
    if (!handled(url) || /^\/m\/\d+$/.test(url.pathname) || url.href === location.href.split("#")[0]) return;
    const p = prefetched.get(url.href);
    if (p && Date.now() - p.at < PREFETCH_TTL) return;
    const entry = { at: Date.now(), load: load(url.href) };
    entry.load.catch(() => prefetched.delete(url.href));
    prefetched.set(url.href, entry);
  }
  document.addEventListener("mouseover", (e) => {
    const a = e.target.closest?.("a[href]");
    if (!a || a.contains(e.relatedTarget)) return;
    clearTimeout(hoverTimer);
    hoverTimer = setTimeout(() => prefetch(a), 80);
  });
  document.addEventListener("mouseout", (e) => {
    const a = e.target.closest?.("a[href]");
    if (a && !a.contains(e.relatedTarget)) clearTimeout(hoverTimer);
  });
  document.addEventListener("touchstart", (e) => {
    const a = e.target.closest?.("a[href]");
    if (a) prefetch(a);
  }, { passive: true });

  history.scrollRestoration = "manual";
  history.replaceState({ ...history.state, nav: current }, "");
  keep(current, initialHTML);
  return { visit };
})();

setupPage();
