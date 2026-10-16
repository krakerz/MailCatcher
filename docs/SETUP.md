# Setup from zero

Everything needed to get mailcatcher receiving and sending mail on your own domain, on the Cloudflare free plan.

Placeholders used below:

| Placeholder | Example | Meaning |
|---|---|---|
| `<domain>` | `example.com` | Your domain, managed by Cloudflare DNS. Every address `*@<domain>` lands in mailcatcher. |
| `<app hostname>` | `mail.example.com` | Public hostname of the app, served through a Cloudflare Tunnel. |

## What you need

- A domain whose DNS is on Cloudflare (free plan is enough).
- A machine with Docker for the app (a NAS works). It needs outbound internet only — no open ports.
- Node.js 20+ on any machine, to deploy the Worker with `wrangler`.
- The mailboxes you want to reply to (e.g. QA Gmail accounts), so you can click their verification links.

How it fits together:

```
sender ──► Cloudflare Email Routing (catch-all on <domain>) ──► Email Worker
             ├─ POST raw mail ──► Cloudflare Tunnel ──► mailcatcher (Docker)
             │     app unreachable → kept in Workers KV
             └─ optional auto-forward to verified addresses
mailcatcher ──► Worker /send ──► Cloudflare ──► verified destination address
```

## 1. Clean up conflicting DNS records

In **DNS → Records** for `<domain>`, check for records that would block mail. A domain that never sent mail often has "parked domain" records like these:

| Record | Problem | Fix |
|---|---|---|
| TXT on `<domain>` starting with `v=spf1` (e.g. `v=spf1 -all`) | Email Routing adds its own SPF record; two SPF records break SPF. | Delete it (or merge `include:_spf.mx.cloudflare.net` into it). |
| TXT on `_dmarc` with `p=reject` and/or `adkim=s; aspf=s` | Too strict while testing. | Change to `v=DMARC1; p=none; sp=none; adkim=r; aspf=r;` — keep any existing `rua=...` part. |
| TXT on `*._domainkey` with `v=DKIM1; p=` | Revokes DKIM keys. | Delete it. |
| Existing `MX` records | Email Routing needs to own MX. | Remove them (only if no other mail service uses the domain). |

## 2. Enable Email Routing

1. **Email → Email Routing → Enable**. Accept the MX and SPF records it offers to add.
2. **Destination addresses → Add** each mailbox that mailcatcher may send to (every address QA replies to). The owner of each mailbox must click the verification link Cloudflare emails them.

> The **Email Sending** page says it needs Workers Paid — you do **not** need it. Sending to verified destination addresses from a Worker works on the free plan with Email Routing alone.

Do not set the catch-all yet; the Worker has to exist first.

## 3. Deploy the Worker

```sh
cd worker
npm install
npx wrangler login
npx wrangler kv namespace create MAILCATCHER   # prints an id
cp wrangler.example.toml wrangler.toml
```

Edit `worker/wrangler.toml` (it is gitignored, your values stay local):

- `kv_namespaces` → put the printed id into `id`. Keep `binding = "KV"`; ignore the binding name wrangler suggests.
- `INGEST_URL` → `https://<app hostname>/internal/ingest`.

Create the shared secret and deploy:

```sh
openssl rand -hex 32                 # keep this value, it also goes into .env
npx wrangler secret put HMAC_SECRET  # paste it
npx wrangler deploy
```

On the first deploy wrangler may ask to register a `workers.dev` subdomain — say yes. Note the printed Worker URL (`https://mailcatcher.<subdomain>.workers.dev`); the app sends mail through it.

## 4. Route all mail to the Worker

**Email → Email Routing → Routing rules → Catch-all address** → action **Send to a Worker** → `mailcatcher` → enable.

From now on every mail to `*@<domain>` reaches the Worker. If the app is not reachable, mail is kept in Workers KV (automatic pickup by the app is upcoming).

## 5. Create the Cloudflare Tunnel

1. **Zero Trust → Networks → Tunnels → Create a tunnel** → type **Cloudflared**, name it `mailcatcher`.
2. Choose the **Docker** install option and copy only the token (the long string after `--token`).
3. **Public hostname**: subdomain/domain = `<app hostname>`, service **HTTP** `mailcatcher:8080`.

The tunnel's DNS record overrides a wildcard (`*`) record if you have one. If you put Cloudflare Access in front of `<app hostname>`, add a bypass for `/internal/*` — the Worker cannot log in.

## 6. Run the app

On the Docker host, in a copy of this repository:

```sh
cp .env.example .env
```

Fill in `.env`:

| Variable | Value |
|---|---|
| `TUNNEL_TOKEN` | Token from step 5. |
| `MC_HMAC_SECRET` | The same value you gave `wrangler secret put HMAC_SECRET`. |
| `MC_DOMAIN` | `<domain>` |

```sh
docker compose up -d
docker compose logs -f mailcatcher
```

Data (database, raw mail, attachments) is stored in `./data`.

## 7. Check it works

1. `curl https://<app hostname>/healthz` → `ok <version>`.
2. From any mailbox send a mail to `test@<domain>`. The app log shows an `ingest` line with its id.
3. If nothing arrives, run `npx wrangler tail` in `worker/` while sending again — it shows each mail and any error.

## Limits and tips

- **Outbound only to verified destination addresses** (max 200 per Cloudflare account). Sending anywhere else fails with `destination address is not a verified address`.
- **Inbound size**: 25 MiB per message including encoding, so attachments top out around 18 MB.
- **Spam folder**: a new domain's mail can land in spam even with SPF/DKIM/DMARC passing. In each receiving Gmail account, create a filter `from:(@<domain>)` → *Never send it to Spam*.
- **Cloudflare API token** (for managing destination addresses from the app — upcoming): My Profile → API Tokens → Custom token with *Account → Email Routing Addresses: Edit* and *Account → Workers KV Storage: Edit*.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Worker log shows `ingest 401` | `MC_HMAC_SECRET` in `.env` differs from the Worker's `HMAC_SECRET`. |
| Worker log shows `ingest 404` / `530` | Tunnel down, wrong public hostname, or wrong `INGEST_URL`. Mail is kept in KV meanwhile. |
| Sender gets `552 5.3.4 Email data size exceeded` | Message over 25 MiB after encoding. |
| Verification email for a destination never arrives | Check spam; adding the same address again re-sends it (rate-limited). |
