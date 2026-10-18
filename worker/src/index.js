import { EmailMessage } from "cloudflare:email";
import { matchRules } from "./rules.js";

const enc = new TextEncoder();

// Free-plan KV allows 1,000 lists and 1,000 writes a day but 100,000 reads. A stash
// sets FLAG (time of the last stash) so the app's minute poll can skip KV.list
// when nothing waits; the flag is cleared once a list comes back empty and the
// last stash is old enough to be visible everywhere (KV takes up to ~60 s).
const FLAG = "pending_flag";
const FLAG_SETTLE_MS = 3 * 60 * 1000;

// putIfChanged spends a (scarce) write only when the value really changed.
async function putIfChanged(env, key, value) {
  if ((await env.KV.get(key)) !== value) await env.KV.put(key, value);
}

async function sign(secret, ts, body) {
  const key = await crypto.subtle.importKey("raw", enc.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const data = new Uint8Array([...enc.encode(ts + "."), ...new Uint8Array(body)]);
  const sig = await crypto.subtle.sign("HMAC", key, data);
  return [...new Uint8Array(sig)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function verify(secret, req, body) {
  const ts = req.headers.get("X-MC-Timestamp") || "";
  // 5-minute window against replay.
  if (Math.abs(Date.now() / 1000 - Number(ts)) > 300) return false;
  const want = await sign(secret, ts, body);
  const got = req.headers.get("X-MC-Signature") || "";
  return want.length === got.length && crypto.subtle.timingSafeEqual(enc.encode(want), enc.encode(got));
}

async function ingest(env, from, to, raw, forwarded) {
  const ts = String(Math.floor(Date.now() / 1000));
  // The app pushes its own URL (MC_PUBLIC_URL); INGEST_URL only covers the time before that.
  const url = (await env.KV.get("ingest_url")) || env.INGEST_URL;
  const res = await fetch(url, {
    method: "POST",
    headers: {
      "Content-Type": "message/rfc822",
      "X-MC-Envelope-From": from,
      "X-MC-Envelope-To": to,
      "X-MC-Auto-Forward": forwarded.join(","),
      "X-MC-Timestamp": ts,
      "X-MC-Signature": await sign(env.HMAC_SECRET, ts, raw),
    },
    body: raw,
  });
  // 422 = the app can never parse it; stashing would only make the drain retry forever.
  if (res.status === 422) return console.log("ingest: unparseable, dropped", from, to);
  if (!res.ok) throw new Error(`ingest ${res.status}`);
}

export default {
  async email(message, env, ctx) {
    const raw = await new Response(message.raw).arrayBuffer();
    const forwardTo = matchRules((await env.KV.get("rules", "json")) || [], message.to);
    try {
      await ingest(env, message.from, message.to, raw, forwardTo);
    } catch (err) {
      const key = `pending:${Date.now()}:${crypto.randomUUID()}`;
      await env.KV.put(key, raw, { metadata: { from: message.from, to: message.to, forwarded: forwardTo } });
      await env.KV.put(FLAG, String(Date.now()));
    }
    // One failing destination (e.g. no longer verified) must not stop the others.
    for (const dest of forwardTo) {
      try {
        await message.forward(dest);
      } catch (err) {
        console.log("forward failed", dest, String(err.message || err));
      }
    }
  },

  async fetch(req, env) {
    const url = new URL(req.url);
    const body = await req.arrayBuffer();
    if (!(await verify(env.HMAC_SECRET, req, body))) return new Response("unauthorized", { status: 401 });

    if (req.method === "POST" && url.pathname === "/send") {
      const from = req.headers.get("X-MC-Envelope-From");
      const to = req.headers.get("X-MC-Envelope-To");
      try {
        // Cloudflare replaces the Message-ID; the result carries the one actually used.
        const res = await env.SEND.send(new EmailMessage(from, to, new TextDecoder().decode(body)));
        return Response.json(res ?? {});
      } catch (err) {
        return new Response(String(err.message || err), { status: 502 });
      }
    }

    // ?all=1 lists even without the flag (the app's hourly safety net).
    if (req.method === "GET" && url.pathname === "/pending") {
      const flag = Number(await env.KV.get(FLAG)) || 0;
      if (!flag && url.searchParams.get("all") !== "1") return Response.json([]);
      const list = await env.KV.list({ prefix: "pending:", limit: 50 });
      if (flag && !list.keys.length && Date.now() - flag > FLAG_SETTLE_MS) await env.KV.delete(FLAG);
      return Response.json(list.keys.map((k) => ({ key: k.name, ...k.metadata })));
    }

    const m = url.pathname.match(/^\/pending\/(pending:.+)$/);
    if (m && req.method === "GET") {
      const raw = await env.KV.get(decodeURIComponent(m[1]), "arrayBuffer");
      return raw ? new Response(raw, { headers: { "Content-Type": "message/rfc822" } }) : new Response("not found", { status: 404 });
    }
    if (m && req.method === "DELETE") {
      await env.KV.delete(decodeURIComponent(m[1]));
      return new Response("deleted");
    }

    if (req.method === "PUT" && url.pathname === "/config") {
      let cfg;
      try {
        cfg = JSON.parse(new TextDecoder().decode(body));
      } catch {}
      const ingestUrl = cfg && cfg.ingest_url;
      if (typeof ingestUrl !== "string" || !/^https:\/\/[^/\s]+\/internal\/ingest$/.test(ingestUrl)) {
        return new Response("ingest_url must be https://<host>/internal/ingest", { status: 400 });
      }
      await putIfChanged(env, "ingest_url", ingestUrl);
      return Response.json({ ingest_url: ingestUrl });
    }

    if (req.method === "PUT" && url.pathname === "/rules") {
      let rules;
      try {
        rules = JSON.parse(new TextDecoder().decode(body));
      } catch {}
      if (!Array.isArray(rules)) return new Response("rules must be a JSON array", { status: 400 });
      await putIfChanged(env, "rules", JSON.stringify(rules));
      return Response.json({ rules: rules.length });
    }

    return new Response("not found", { status: 404 });
  },
};
