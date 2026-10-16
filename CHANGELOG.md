# Changelog

## [Unreleased]

### Added
- Project scaffold: Go app skeleton with health endpoint, Cloudflare Email Worker, Docker Compose with cloudflared
- Incoming mail is stored with its raw source, attachments, extracted links and verification codes
- Step-by-step setup guide from zero in `docs/SETUP.md`
- Replies are linked to the message they answer; repeated deliveries of the same mail are stored once
