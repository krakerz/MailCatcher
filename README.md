# mailcatcher

Self-hosted Mailosaur-style email catcher for QA: catch-all inbox, web UI, reply and forward, test API.

## Description

mailcatcher receives every email sent to any address on your domain through Cloudflare Email Routing (catch-all) and an Email Worker, delivers it through a Cloudflare Tunnel to a Go app running in Docker (e.g. on a NAS). QA logs into a web UI to read, reply, compose and forward. Outbound mail goes through the Worker's send_email binding, only to Cloudflare-verified destination addresses (free tier).

## Features

*Work in progress — planned features:*

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

Health check: `curl http://localhost:8080/healthz`.

More to come.

## FAQ

**Why only verified destination addresses?**

Cloudflare sends to verified destinations for free; arbitrary recipients need a paid plan.

---

### Notes

Built and maintained with the help of AI.
