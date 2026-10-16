import { EmailMessage } from "cloudflare:email";

const enc = new TextEncoder();

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

async function ingest(env, from, to, raw) {
  const ts = String(Math.floor(Date.now() / 1000));
  const res = await fetch(env.INGEST_URL, {
    method: "POST",
    headers: {
      "Content-Type": "message/rfc822",
      "X-MC-Envelope-From": from,
      "X-MC-Envelope-To": to,
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
    try {
      await ingest(env, message.from, message.to, raw);
    } catch (err) {
      const key = `pending:${Date.now()}:${crypto.randomUUID()}`;
      await env.KV.put(key, raw, { metadata: { from: message.from, to: message.to } });
    }

    const rules = (await env.KV.get("rules", "json")) || [];
    for (const r of rules) {
      if (r.enabled && new RegExp(r.pattern, "i").test(message.to)) {
        await message.forward(r.destination);
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

    if (req.method === "GET" && url.pathname === "/pending") {
      const list = await env.KV.list({ prefix: "pending:", limit: 50 });
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

    if (req.method === "PUT" && url.pathname === "/rules") {
      await env.KV.put("rules", body);
      return new Response("ok");
    }

    return new Response("not found", { status: 404 });
  },
};
