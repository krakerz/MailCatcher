# Changelog

## [Unreleased]

## [0.1.0] — 2026-10-17

### Added
- Project scaffold: Go app, Cloudflare Email Worker, Docker Compose with cloudflared
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
- A recipient verified moments ago is recognised right away; a clear message when Cloudflare has not activated it yet
- Sending to an unverified address is refused before sending, with a one-click verification request; To fields suggest verified addresses
- Auto-forward rules by inbox and address pattern, applied by the Worker on arrival; rule tester; messages show where they were auto-forwarded
- Mail kept in Workers KV while the app was unreachable is collected automatically every minute
- Retention: messages older than `MC_RETENTION_DAYS` (default 30) are deleted
- `MC_PUBLIC_URL`: the app tells the Worker where to deliver mail, so the hostname can change without redeploying the Worker
- Pages are never cached or modified by Cloudflare (no injected analytics, no email obfuscation)
- Docker healthcheck; cloudflared starts once the app is healthy; configurable data-folder owner
- New MailCatcher interface: sidebar layout, light and dark theme, app logo, icons, mobile menu
- Verified destination addresses are picked from a dropdown; API keys get a copy button
- Inbox address lists: pin specific catch-all addresses (with `*` wildcards) to an inbox; the form warns when a list prefix belongs in the format (`{inbox}`)
- Per-user inbox access: limit a user to selected inboxes; users manage forward rules for their inboxes
- Rich editor for reply and compose: formatting, links and inline images (sent as HTML with a text version)
- Per-user read state: unread mail is highlighted and counted, with mark unread and mark all read
- Inbox Refresh button collects mail still waiting at Cloudflare
- Forward rules match the real recipient (e.g. `any@example.com`) in each inbox's address format; overlapping rules forward once per destination; rules that can never fire are flagged
- Testing an address on the Rules page warns when it is malformed or its domain is not one of yours
- Select messages in the inbox and Sent lists to mark read, mark unread or delete them; delete from the message page
- Each inbox has Received and Sent tabs; the selection bar offers Mark read, Mark unread and Delete, or Mark all read when nothing is selected
- Clicking anywhere on a message row opens it
- Tables scroll sideways on phones
- Changing a user's role saves immediately and asks before demoting an admin
- Only admins can add destination addresses and send verification links
- Inline images in the editor can be resized; large images are shrunk automatically
- Ctrl+Enter (Cmd+Enter) sends a reply, compose or forward
- Empty lists show their message centred
- The inbox form tells where mail to an example address really lands, and flags conflicts with other inboxes
- File pickers follow the light/dark theme
- Reply and forward pages show the original message
- API reference in `docs/API.md`
- Sent view with who sent each message; messages list the replies and forwards sent from them
- Test API with per-inbox API keys: list/filter messages with long-poll wait, fetch message, source and attachments, delete
