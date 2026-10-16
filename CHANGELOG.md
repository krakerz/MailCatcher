# Changelog

## [Unreleased]

### Added
- Project scaffold: Go app skeleton with health endpoint, Cloudflare Email Worker, Docker Compose with cloudflared
- Incoming mail is stored with its raw source, attachments, extracted links and verification codes
- Step-by-step setup guide from zero in `docs/SETUP.md`
- Replies are linked to the message they answer; repeated deliveries of the same mail are stored once
- Web UI with login: live-updating inbox with search, message view (sandboxed HTML, text, headers, links, codes, attachments, source)
- User management for admins: add users with one-time temporary passwords, reset passwords, disable, change role
- First start creates an `admin` user; forced password change for generated and reset passwords
- Login rate limiting and CSRF protection
- Virtual inboxes with configurable address formats; incoming mail is routed by format and the real recipient is decoded (e.g. `b.gmail@…` → `b@gmail.com`)
- Inbox management for admins with live format preview, default inbox, own domain per inbox, and re-applying routing to stored mail
- Reply, forward and compose from the web UI and API, sent through the Worker to verified destination addresses; replies thread with the original
- Destinations page: verified addresses synced from Cloudflare, request verification for new ones, remove (admins)
- Sending to an unverified address is refused before sending, with a one-click verification request; To fields suggest verified addresses
- Reply and forward pages show the original message
- API reference in `docs/API.md`
- Sent view with who sent each message; messages list the replies and forwards sent from them
- Test API with per-inbox API keys: list/filter messages with long-poll wait, fetch message, source and attachments, delete
