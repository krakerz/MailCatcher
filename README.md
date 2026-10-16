# mailcatcher

Self-hosted Mailosaur-style email catcher for QA: catch-all inbox, web UI, reply and forward, test API.

## Description

mailcatcher receives every email sent to any address on your domain through Cloudflare Email Routing (catch-all) and an Email Worker, delivers it through a Cloudflare Tunnel to a Go app running in Docker (e.g. on a NAS). QA logs into a web UI to read, reply, compose and forward. Outbound mail goes through the Worker's send_email binding, only to Cloudflare-verified destination addresses (free tier).

## Features

*Work in progress — not all listed features exist yet:*

- Catch-all inbox for a whole domain
- Virtual inboxes by address list or address format (e.g. `b.gmail`, `b=gmail.com`); the real recipient is decoded and shown
- Web UI with login, light/dark theme and user management, including per-user inbox access
- Reply, compose and forward to verified addresses
- Destination-address management (Cloudflare API) from the UI, with a verified check before sending
- Auto-forward rules, applied by the Worker as mail arrives (also while the app is offline)
- Test API with long-polling wait-for-message
- Nothing is lost while the app is offline: the Worker keeps mail in Workers KV and the app collects it when it is back

## Installation

Full walkthrough from zero (Cloudflare DNS, Email Routing, Worker, Tunnel, Docker): **[docs/SETUP.md](docs/SETUP.md)**.

Short version, once Cloudflare is set up:

1. Copy `.env.example` to `.env` and fill in the values.
2. Create the data folder as the user in `MC_UID`/`MC_GID`: `mkdir data`.
3. Run `docker compose up -d --build`.

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

Open `https://<app hostname>` and log in. The inbox updates live as mail arrives (**Refresh** also collects anything still waiting at Cloudflare); unread mail is bold and counted in the sidebar, per user, with **Mark unread** and **Mark all read** for shared inboxes; tick messages to mark them read/unread or delete them; open a message to see its HTML (sandboxed, remote images blocked until you allow them), plain text, headers, links, verification codes and attachments.

**Sending** (free plan: only to [verified destination addresses](docs/SETUP.md#2-enable-email-routing)):

- **Reply** — from the catch-all address the mail was sent to (e.g. `b.gmail@<domain>`) to the original sender (its `Reply-To` if set), threaded and optionally quoting the original.
- **Forward** — to a verified address with the original attached as `.eml`; replies go to the original sender, or back into mailcatcher if you choose.
- **Compose** — pick an inbox and the real address to send as (`b@gmail.com`); it is sent from the matching catch-all address.
- Reply and compose have a basic rich editor (bold, italic, lists, quote, links, inline images — paste or pick them) and send HTML with a plain-text version; several attachments per mail, 18 MB in total.
- **Sent** lists everything sent, by whom and from which inbox (filter per inbox, or the Sent tab of an inbox); a message shows the replies and forwards sent from it.
- **Destinations** lists the verified addresses and lets anyone request verification for a new one (Cloudflare emails its owner a link); admins can remove addresses. A send to an unverified address offers that request right away, and the To fields suggest verified addresses.

**Forward rules**: forward incoming mail automatically to verified addresses. A rule names an inbox (or any inbox) and a **real recipient** such as `any@example.com` (`*` allowed before the `@`): it matches the catch-all address that recipient is encoded to in that inbox — for "any inbox", in every inbox with its own format. Overlapping rules forward once per destination; different destinations each get a copy. A catch-all pattern on your own domain (`team1.*@<domain>`) works too. The Worker applies them as mail arrives, even when the app is offline; replies from the destination go straight to the original sender. The page has a box to test which rules an address hits, and messages show where they were auto-forwarded. Users manage the rules of the inboxes they can open; rules for "any inbox" need unrestricted access.

**Users** (admins only): add users, reset passwords, disable accounts, change roles, and limit a user to some inboxes (none ticked = all). *Users* read, reply, compose and forward in the inboxes they can open; *admins* see everything and manage users, inboxes and rules.

The interface has a light and a dark theme (follows the system by default; switch at the bottom of the sidebar) and works on phones.

### Inboxes and address formats

Mail to any address on the catch-all domain lands in an inbox. Each inbox has an **address format** that maps the catch-all address back to the real address it stands for, so `b.gmail@<domain>` is shown as `b@gmail.com`:

| Format | `b@gmail.com` becomes | Notes |
|---|---|---|
| `{local}.{name}` | `b.gmail@<domain>` | Mailosaur style; needs an assumed TLD (`com`), so only `<name>.com` addresses fit. |
| `{local}={domain}` | `b=gmail.com@<domain>` | Any domain. |
| `{local}+{domain}` | `b+gmail.com@<domain>` | Any domain. |
| `{inbox}-{local}={domain}` | `team1-b=gmail.com@<domain>` | The `{inbox}` marker (the inbox slug) lets several inboxes share one domain. |

Tokens: `{local}`, `{domain}`, `{name}` (domain without TLD), `{tld}`, `{inbox}`. Admins manage inboxes under **Inboxes**; the form shows a live preview and warns about ambiguous formats.

**One inbox per team or app:** give each inbox a slug and a format with the `{inbox}` marker, e.g. slug `team1` and `{inbox}-{domain}~{local}`: `b@gmail.com` is sent to as `team1-gmail.com~b@<domain>`, lands in that inbox and is shown as `b@gmail.com`; replies from MailCatcher come from that address, so the sending app can decode it back the same way.

**Address lists** pin specific addresses to an inbox: one catch-all address per line, `*` as wildcard. For example Default (`{local}.{name}`), Inbox A with `b.gmail@<domain>`, Inbox C with `support@<domain>`: mail to `a.gmail@…` lands in Default, `b.gmail@…` in Inbox A (still shown as `b@gmail.com`), `support@…` in Inbox C.

Routing order: inboxes with an address list (by priority, lowest first), then inboxes matched by domain and format (by priority), and the default inbox takes the rest. An inbox can also have its own domain (that domain needs its own Email Routing catch-all to the Worker). Changing formats affects new mail; **Re-apply routing** re-sorts stored mail.

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
| `MC_PUBLIC_URL` | — | The app's public URL (tunnel hostname, e.g. `https://inbox.example.com`). Pushed to the Worker as the delivery target, so changing the hostname needs no Worker redeploy. |
| `MC_WORKER_URL` | — | Worker URL for sending; empty disables reply/compose/forward. |
| `MC_ADMIN_PASSWORD` | random | First admin's password, used on first start only. |
| `MC_RETENTION_DAYS` | `30` | Delete messages older than this many days (checked hourly); `0` keeps everything. |
| `MC_UID`, `MC_GID` | `1000` | Compose only: host user/group that owns `./data`. |
| `MC_DATA_DIR` | `/data` | Database, raw mail and attachments. |
| `MC_LISTEN` | `:8080` | Listen address. |
| `MC_COOKIE_SECURE` | `true` | Set `false` only for plain-http local testing. |

## FAQ

**Why only verified destination addresses?**

Cloudflare sends to verified destinations for free; arbitrary recipients need a paid plan.

---

### Notes

Built and maintained with the help of AI.
