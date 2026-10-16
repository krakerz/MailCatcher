# mailcatcher

Self-hosted Mailosaur-style email catcher for QA: catch-all inbox, web UI, reply and forward, test API.

## Description

mailcatcher receives every email sent to any address on your domain through Cloudflare Email Routing (catch-all) and an Email Worker, delivers it through a Cloudflare Tunnel to a Go app running in Docker (e.g. on a NAS). QA logs into a web UI to read, reply, compose and forward. Outbound mail goes through the Worker's send_email binding, only to Cloudflare-verified destination addresses (free tier).

## Features

*Work in progress — not all listed features exist yet:*

- Catch-all inbox for a whole domain
- Virtual inboxes with configurable address formats (e.g. `b.gmail`, `b=gmail.com`)
- Web UI with login and user management
- Reply, compose and forward to verified addresses
- Destination-address management (Cloudflare API) from the UI
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

**Users** (admins only): add QA users, reset passwords, disable accounts, change roles. *QA* users read mail; *admins* also manage users and inboxes.

### Test API

Generate a key under **Inboxes → Generate**, then send it as a Bearer token. A key only sees its own inbox.

```sh
# Wait up to 30 s for a mail to b.gmail@<domain>, return as soon as it arrives
curl -H "Authorization: Bearer $MC_API_KEY" \
  "https://<app hostname>/api/inboxes/1/messages?to=b.gmail@<domain>&wait=30s"
```

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/inboxes/{id}/messages` | List messages, newest first. Filters: `to`, `from`, `subject` (substring, case-insensitive), `since` (RFC 3339 or unix time), `limit` (default 50, max 500). `wait=30s` blocks until something matches (max 90 s); on timeout returns an empty list. |
| `GET` | `/api/messages/{id}` | Full message: addresses, subject, `text`, `html`, `links`, `codes`, `attachments` (with download `url`). |
| `GET` | `/api/messages/{id}/raw` | Original source (`message/rfc822`). |
| `GET` | `/api/messages/{id}/attachments/{aid}` | Download one attachment. |
| `DELETE` | `/api/inboxes/{id}/messages` | Delete messages matching the same filters (all of the inbox without filters). Returns `{"deleted": n}`. |

Tip: pass `since=<test start time>` so a test never picks up mail from an earlier run.

### Configuration

| Variable | Default | Description |
|---|---|---|
| `MC_DOMAIN` | — | Catch-all domain (required). |
| `MC_HMAC_SECRET` | — | Shared secret with the Worker, 32+ characters (required). |
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
