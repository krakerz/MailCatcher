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
- Test API with per-inbox API keys: list/filter messages with long-poll wait, fetch message, source and attachments, delete
