# mailcatcher

Self-hosted Mailosaur-style email catcher for QA: catch-all inbox, web UI, reply and forward, test API.

## Description

mailcatcher receives every email sent to any address on your domain through Cloudflare Email Routing (catch-all) and an Email Worker, delivers it through a Cloudflare Tunnel to a Go app running in Docker (e.g. on a NAS). QA logs into a web UI to read, reply, compose and forward. Outbound mail goes through the Worker's send_email binding, only to Cloudflare-verified destination addresses (free tier).

## Features

*Work in progress — not all listed features exist yet:*

- Catch-all inbox for a whole domain
- Virtual inboxes with configurable address formats (e.g. `b.gmail`, `b=gmail.com`); the real recipient is decoded and shown
- Web UI with login and user management
- Reply, compose and forward to verified addresses
- Destination-address management (Cloudflare API) from the UI, with a verified check before sending
- Auto-forward rules
- Test API with long-polling wait-for-message
- Fallback storage in Workers KV when the app is offline

## Installation

Full walkthrough from zero (Cloudflare DNS, Email Routing, Worker, Tunnel, Docker): **[docs/SETUP.md](docs/SETUP.md)**.

Short version, once Cloudflare is set up:

1. Copy `.env.example` to `.env` and fill in the values.
2. Run `docker compose up -d`.

## Building from source

Prerequisites:

- Go 1.27+
- Node 20+ (for the Worker)
- Docker

Build the Go app:

```sh
go build ./cmd/mailcatcher
```

Deploy the Worker:

```sh
cd worker && npm install && cp wrangler.example.toml wrangler.toml   # fill in KV id + app hostname, see docs/SETUP.md
npx wrangler deploy
```

## Usage

Open `https://<app hostname>` and log in. The inbox updates live as mail arrives; open a message to see its HTML (sandboxed, remote images blocked until you allow them), plain text, headers, links, verification codes and attachments.

**Sending** (free plan: only to [verified destination addresses](docs/SETUP.md#2-enable-email-routing)):

- **Reply** — from the catch-all address the mail was sent to (e.g. `b.gmail@<domain>`) to the original sender (its `Reply-To` if set), threaded and optionally quoting the original.
- **Forward** — to a verified address with the original attached as `.eml`; replies go to the original sender, or back into mailcatcher if you choose.
- **Compose** — pick an inbox and the real address to send as (`b@gmail.com`); it is sent from the matching catch-all address.
- **Sent** lists everything sent, by whom; a message shows the replies and forwards sent from it.
- **Destinations** lists the verified addresses and lets anyone request verification for a new one (Cloudflare emails its owner a link); admins can remove addresses. A send to an unverified address offers that request right away, and the To fields suggest verified addresses.

**Users** (admins only): add users, reset passwords, disable accounts, change roles. *Users* read mail (and later reply, compose and forward); *admins* also manage users and inboxes.

### Inboxes and address formats

Mail to any address on the catch-all domain lands in an inbox. Each inbox has an **address format** that maps the catch-all address back to the real address it stands for, so `b.gmail@<domain>` is shown as `b@gmail.com`:

| Format | `b@gmail.com` becomes | Notes |
|---|---|---|
| `{local}.{name}` | `b.gmail@<domain>` | Mailosaur style; needs an assumed TLD (`com`), so only `<name>.com` addresses fit. |
| `{local}={domain}` | `b=gmail.com@<domain>` | Any domain. |
| `{local}+{domain}` | `b+gmail.com@<domain>` | Any domain. |
| `{inbox}-{local}={domain}` | `team1-b=gmail.com@<domain>` | The `{inbox}` marker (the inbox slug) lets several inboxes share one domain. |

Tokens: `{local}`, `{domain}`, `{name}` (domain without TLD), `{tld}`, `{inbox}`. Admins manage inboxes under **Inboxes**; the form shows a live preview and warns about ambiguous formats.

Routing: inboxes are tried in priority order (lowest first) and the first whose domain and format match receives the mail; anything else goes to the default inbox. An inbox can also have its own domain (that domain needs its own Email Routing catch-all to the Worker). Changing formats affects new mail; **Re-apply routing** re-sorts stored mail.

### Test API

Full reference with examples: **[docs/API.md](docs/API.md)**.

Generate a key under **Inboxes → Generate**, then send it as a Bearer token. A key only sees its own inbox.

```sh
# Wait up to 30 s for a mail to b@gmail.com (sent as b.gmail@<domain>), return as soon as it arrives
curl -H "Authorization: Bearer $MC_API_KEY" \
  "https://<app hostname>/api/inboxes/1/messages?to=b@gmail.com&wait=30s"
```

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/inboxes/{id}/messages` | List messages, newest first. Filters: `to` (catch-all or decoded real address), `from`, `subject` (substring, case-insensitive), `since` (RFC 3339 or unix time), `limit` (default 50, max 500), `direction` (`in` default, `out`, `all`). `wait=30s` blocks until something matches (max 90 s); on timeout returns an empty list. |
| `GET` | `/api/messages/{id}` | Full message: addresses, subject, `text`, `html`, `links`, `codes`, `attachments` (with download `url`). |
| `GET` | `/api/messages/{id}/raw` | Original source (`message/rfc822`). |
| `GET` | `/api/messages/{id}/attachments/{aid}` | Download one attachment. |
| `POST` | `/api/messages/{id}/reply` | Reply to the sender. Body `{"text": "...", "quote": true}`. Returns the sent message (`201`); `422` if the recipient is not a verified destination. |
| `POST` | `/api/messages/{id}/forward` | Forward to a verified address. Body `{"to": "...", "text": "note", "reply_to_mailcatcher": false}`. |
| `DELETE` | `/api/inboxes/{id}/messages` | Delete matching messages, received and sent unless `direction` is given (the whole inbox without filters). Returns `{"deleted": n}`. |

Tip: pass `since=<test start time>` so a test never picks up mail from an earlier run.

### Configuration

| Variable | Default | Description |
|---|---|---|
| `MC_DOMAIN` | — | Catch-all domain (required). |
| `MC_HMAC_SECRET` | — | Shared secret with the Worker, 32+ characters (required). |
| `MC_CF_API_TOKEN` | — | Cloudflare token (Email Routing Addresses: Edit) to manage destination addresses from the app. |
| `MC_CF_ACCOUNT_ID` | — | Cloudflare account ID for the above. |
| `MC_WORKER_URL` | — | Worker URL for sending; empty disables reply/compose/forward. |
| `MC_ADMIN_PASSWORD` | random | First admin's password, used on first start only. |
| `MC_DATA_DIR` | `/data` | Database, raw mail and attachments. |
| `MC_LISTEN` | `:8080` | Listen address. |
| `MC_COOKIE_SECURE` | `true` | Set `false` only for plain-http local testing. |

## FAQ

**Why only verified destination addresses?**

Cloudflare sends to verified destinations for free; arbitrary recipients need a paid plan.

---

### Notes

Built and maintained with the help of AI.
