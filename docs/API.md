# Test API

HTTP API for test automation. JSON over HTTPS; every endpoint requires a per-inbox API key.

## Authentication

Each inbox has its own API key. An admin creates it under **Inboxes → Generate** (the button reads **Regenerate** once a key exists). The key is shown once; only its hash is stored. Regenerating a key revokes the previous one immediately.

Keys look like `mc_<random>`. Send them as a Bearer token:

```sh
curl -H "Authorization: Bearer $MC_API_KEY" "https://<app hostname>/api/inboxes/<inbox id>/messages"
```

- A key only sees its own inbox. Using another inbox's id in the path returns `403`.
- A message that belongs to another inbox is reported as `404`, the same as a message that does not exist.
- `401` is returned when the `Authorization` header is missing, does not start with `Bearer ` (the prefix is case-sensitive), has an empty token, or the token matches no inbox.

## Base URL

`https://<app hostname>`

All paths in this document are relative to the base URL and start with `/api`.

## Endpoints

### List messages

`GET /api/inboxes/{id}/messages`

Returns messages of the key's inbox, newest first. `{id}` must be the key's own inbox id.

Items in `messages` are summaries: they have no `text`, `html` or `attachments` array. Use `attachment_count` to see whether there are attachments, and [get message](#get-message) for the full content.

Query parameters:

| Name | Type | Default | Description |
|---|---|---|---|
| `to` | string | — | Case-insensitive substring. Matches the envelope recipient, the `To` and `Cc` headers, and the decoded real address. Either `b.gmail@<domain>` or `b@gmail.com` works. |
| `from` | string | — | Case-insensitive substring. Matches the envelope sender, the `From` address and the `From` name. |
| `subject` | string | — | Case-insensitive substring of the subject. |
| `since` | string | — | Only messages received at or after this time. RFC 3339 (`2026-10-16T10:00:00Z`), unix seconds (`1789552800`), or unix milliseconds when the value is greater than 1e12 (`1789552800000`). |
| `limit` | integer | `50` | Maximum number of messages. Positive integer, maximum `500`. |
| `direction` | string | `in` | `in` (received), `out` (sent by mailcatcher from this inbox), or `all`. |
| `wait` | string | — | Go duration such as `30s` or `2m`. Blocks until at least one message matches, then returns immediately. Capped at `90s`. On timeout returns `200` with an empty `messages` array. `wait=0` (or no `wait`) returns at once. |

Success: `200 OK`

Response wrapper: `{"messages": [...]}`. The array is empty when nothing matches.

Example response:

```json
{
  "messages": [
    {
      "id": 412,
      "inbox_id": 1,
      "direction": "in",
      "message_id": "<8c1f2a@mail.example.net>",
      "envelope_from": "noreply@example.com",
      "envelope_to": "b.gmail@<domain>",
      "decoded_to": "b@gmail.com",
      "from": {"name": "Example", "address": "noreply@example.com"},
      "to": [{"address": "b.gmail@<domain>"}],
      "cc": [],
      "subject": "Your verification code",
      "date": "2026-10-16T10:02:11Z",
      "received_at": "2026-10-16T10:02:12.418Z",
      "size": 5230,
      "links": ["https://example.com/verify?token=abc"],
      "codes": ["493017"],
      "attachment_count": 0,
      "auto_forwarded": []
    }
  ]
}
```

Notes on the fields: `decoded_to` is omitted when empty. `name` inside an address is omitted when empty. `date` is `null` when the message has no `Date` header. `attachment_count` counts non-inline attachments only. `auto_forwarded` lists the addresses the Worker forwarded an incoming message to by a forward rule.

Errors: `400` (invalid `direction`, `since`, `limit` or `wait`), `401`, `403`, `500`.

### Get message

`GET /api/messages/{id}`

Returns the full message: all summary fields plus headers, bodies and attachments. `{id}` is the message id from a list result.

Success: `200 OK`

Path parameter: `id` (integer). A non-numeric id returns `404`.

Additional fields compared with a summary:

| Field | Type | Description |
|---|---|---|
| `in_reply_to` | string | `In-Reply-To` header. Empty when absent. |
| `references` | string[] | `References` header entries. `[]` when absent. |
| `parent_id` | integer or null | For replies and forwards sent from mailcatcher, the id of the message they answer. |
| `text` | string | Plain-text body. |
| `html` | string | HTML body. |
| `attachments` | object[] | All attachments, including inline ones (see below). |

Each attachment object:

| Field | Type | Description |
|---|---|---|
| `id` | integer | Attachment id. |
| `filename` | string | Filename from the message. |
| `content_type` | string | Media type. |
| `size` | integer | Size in bytes. |
| `content_id` | string | `Content-ID`, present only for inline attachments that have one. |
| `inline` | boolean | `true` for inline parts. |
| `url` | string | Path relative to the base URL, for [download attachment](#download-attachment). |

Example response:

```json
{
  "id": 412,
  "inbox_id": 1,
  "direction": "in",
  "message_id": "<8c1f2a@mail.example.net>",
  "envelope_from": "noreply@example.com",
  "envelope_to": "b.gmail@<domain>",
  "decoded_to": "b@gmail.com",
  "from": {"name": "Example", "address": "noreply@example.com"},
  "to": [{"address": "b.gmail@<domain>"}],
  "cc": [],
  "subject": "Your verification code",
  "date": "2026-10-16T10:02:11Z",
  "received_at": "2026-10-16T10:02:12.418Z",
  "size": 5230,
  "links": ["https://example.com/verify?token=abc"],
  "codes": ["493017"],
  "attachment_count": 1,
  "in_reply_to": "",
  "references": [],
  "parent_id": null,
  "text": "Your code is 493017.\n\nIt expires in 10 minutes.",
  "html": "<p>Your code is <b>493017</b>.</p>",
  "attachments": [
    {
      "id": 77,
      "filename": "terms.pdf",
      "content_type": "application/pdf",
      "size": 18342,
      "inline": false,
      "url": "/api/messages/412/attachments/77"
    }
  ]
}
```

Errors: `401`, `403`, `404` (unknown id, or a message in another inbox), `500`.

### Get raw source

`GET /api/messages/{id}/raw`

Returns the original message source exactly as received (or as sent, for outbound messages).

Success: `200 OK`, `Content-Type: message/rfc822`, body is the raw message.

Errors: `401`, `403`, `404` (unknown id, or a message in another inbox). If the stored file is missing, the response is a plain-text `404` from the file server rather than JSON.

### Download attachment

`GET /api/messages/{id}/attachments/{aid}`

Returns one attachment. `{aid}` is an attachment `id` from [get message](#get-message).

Success: `200 OK`, `Content-Type: application/octet-stream`, `Content-Disposition: attachment; filename="<filename>"`. The body is the attachment content. If the attachment has no filename, the name is `attachment-<aid>`.

Errors: `401`, `403`, `404` (message not found or in another inbox; the attachment id does not belong to that message). The attachment-not-found case is a plain-text `404`, not JSON.

### Reply

`POST /api/messages/{id}/reply`

Sends a reply to the message. The reply is sent from the catch-all address the original was sent to (for example `b.gmail@<domain>`), to the original sender's `Reply-To` if set, otherwise its `From` address. It is threaded with `In-Reply-To` and `References`, and the subject gets a `Re: ` prefix unless it already has one.

Request body (JSON):

| Name | Type | Default | Description |
|---|---|---|---|
| `text` | string | `""` | Reply body. |
| `quote` | boolean | `true` | When `true` and the original has a plain-text body, appends the original text quoted with `> ` under an attribution line. |

Success: `201 Created`. Response is the sent message as a summary (`direction` is `out`).

Example request:

```sh
curl -X POST \
  -H "Authorization: Bearer $MC_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"text": "Thanks, confirmed.", "quote": false}' \
  "https://<app hostname>/api/messages/412/reply"
```

Example response:

```json
{
  "id": 413,
  "inbox_id": 1,
  "direction": "out",
  "message_id": "<d41e09@<domain>>",
  "envelope_from": "b.gmail@<domain>",
  "envelope_to": "noreply@example.com",
  "decoded_to": "b@gmail.com",
  "from": {"address": "b.gmail@<domain>"},
  "to": [{"address": "noreply@example.com"}],
  "cc": [],
  "subject": "Re: Your verification code",
  "date": "2026-10-16T10:03:40Z",
  "received_at": "2026-10-16T10:03:40.902Z",
  "size": 1914,
  "links": [],
  "codes": [],
  "attachment_count": 0,
  "auto_forwarded": []
}
```

Errors:

| Status | Cause |
|---|---|
| `400` | Body is not valid JSON. |
| `401`, `403`, `404` | As for [get message](#get-message). |
| `422` | The recipient is not a Cloudflare-verified destination address. |
| `502` | Other send failure, or the original has no recipient to reply from. |
| `503` | Sending is not configured (`MC_WORKER_URL` is empty). |

### Forward

`POST /api/messages/{id}/forward`

Forwards the message to a verified address. The original is attached as `original.eml`. The body text is a plain-text note placed above a `---------- Forwarded message ----------` header block (From, Date, Subject, To and the original text). The subject gets a `Fwd: ` prefix unless it already has one.

Reply-To: by default the original sender (its `Reply-To` if set, otherwise its `From`), so replies go straight to the sender. With `reply_to_mailcatcher: true`, replies go back to the catch-all address the original was sent to, so they land in mailcatcher.

Request body (JSON):

| Name | Type | Default | Description |
|---|---|---|---|
| `to` | string | — (required) | Destination address. Must be a Cloudflare-verified destination. Empty values return `400`. |
| `text` | string | `""` | Note placed above the forwarded content. |
| `reply_to_mailcatcher` | boolean | `false` | Route replies to the forward back into mailcatcher instead of to the original sender. |

Success: `201 Created`. Response is the sent message as a summary (`direction` is `out`, `parent_id` links it to the original in the full view).

Example request:

```sh
curl -X POST \
  -H "Authorization: Bearer $MC_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"to": "qa@example.com", "text": "Verification mail for review", "reply_to_mailcatcher": true}' \
  "https://<app hostname>/api/messages/412/forward"
```

Example response:

```json
{
  "id": 414,
  "inbox_id": 1,
  "direction": "out",
  "message_id": "<7b2c55@<domain>>",
  "envelope_from": "b.gmail@<domain>",
  "envelope_to": "qa@example.com",
  "decoded_to": "b@gmail.com",
  "from": {"address": "b.gmail@<domain>"},
  "to": [{"address": "qa@example.com"}],
  "cc": [],
  "subject": "Fwd: Your verification code",
  "date": "2026-10-16T10:04:02Z",
  "received_at": "2026-10-16T10:04:02.311Z",
  "size": 6021,
  "links": [],
  "codes": [],
  "attachment_count": 1,
  "auto_forwarded": []
}
```

Errors:

| Status | Cause |
|---|---|
| `400` | Body is not valid JSON, or `to` is missing or empty. |
| `401`, `403`, `404` | As for [get message](#get-message). |
| `422` | `to` is not a Cloudflare-verified destination address. |
| `502` | Other send failure, the original has no recipient to forward from, or the stored original cannot be read. |
| `503` | Sending is not configured. |

### Delete messages

`DELETE /api/inboxes/{id}/messages`

Deletes the messages that match the filters, together with their stored raw source and attachments. Takes the same query parameters as [list messages](#list-messages) except `wait`, which is ignored.

Without `direction`, deletes both received and sent messages that match; pass `direction=in` or `direction=out` to limit it. Without any filter, the whole inbox is emptied. Deleting is permanent.

Success: `200 OK`

Example response:

```json
{"deleted": 3}
```

Example request:

```sh
curl -X DELETE -H "Authorization: Bearer $MC_API_KEY" \
  "https://<app hostname>/api/inboxes/<inbox id>/messages?to=b@gmail.com"
```

Errors: `400` (invalid `direction`, `since` or `limit`), `401`, `403`, `500`.

## Errors

Errors from the API are JSON objects with a single field:

```json
{"error": "api key belongs to another inbox"}
```

The `error` text is for humans; match on the status code. Status codes used:

| Status | Meaning |
|---|---|
| `200` | Success (list, get, raw, attachment, delete). |
| `201` | Reply or forward sent. |
| `400` | Invalid query parameter, invalid JSON body, or missing forward `to`. |
| `401` | Missing or malformed `Authorization` header, or unknown API key. |
| `403` | Key belongs to a different inbox than the id in the path. |
| `404` | Message or attachment not found, or it belongs to another inbox. |
| `422` | Recipient is not a Cloudflare-verified destination address. |
| `500` | Internal error. |
| `502` | Send failed for another reason. |
| `503` | Sending is not configured on this instance. |

## Recipes

Replace `<app hostname>`, `<inbox id>` and `<domain>` with your values, and set `MC_API_KEY` to the inbox key.

### (a) Wait for a verification code

Record the time before triggering the mail. Ask for messages to the test address received since then, blocking up to 60 seconds, and read the first code of the newest match.

curl:

```sh
SINCE=$(date +%s)
# ... trigger the email in the system under test ...
curl -s -H "Authorization: Bearer $MC_API_KEY" \
  "https://<app hostname>/api/inboxes/<inbox id>/messages?to=b@gmail.com&since=$SINCE&wait=60s" \
  | jq -r '.messages[0].codes[0]'
```

Playwright test (TypeScript):

```ts
import { test, expect } from '@playwright/test';

const BASE = 'https://<app hostname>';
const INBOX = '<inbox id>';
const KEY = process.env.MC_API_KEY!;

test('receives a verification code', async () => {
  const since = Date.now(); // unix ms; sent as since=<ms>
  // ... trigger the email in the system under test ...

  const url = `${BASE}/api/inboxes/${INBOX}/messages?to=${encodeURIComponent('b@gmail.com')}&since=${since}&wait=60s`;
  const res = await fetch(url, { headers: { Authorization: `Bearer ${KEY}` } });
  expect(res.status).toBe(200);
  const { messages } = await res.json();
  expect(messages.length).toBeGreaterThan(0);
  const code = messages[0].codes[0];
  expect(code).toBeTruthy();
});
```

Python (requests):

```python
import os, time, requests

BASE = "https://<app hostname>"
INBOX = "<inbox id>"
headers = {"Authorization": f"Bearer {os.environ['MC_API_KEY']}"}

since = int(time.time())
# ... trigger the email in the system under test ...

r = requests.get(
    f"{BASE}/api/inboxes/{INBOX}/messages",
    headers=headers,
    params={"to": "b@gmail.com", "since": since, "wait": "60s"},
    timeout=100,  # longer than wait
)
r.raise_for_status()
messages = r.json()["messages"]
code = messages[0]["codes"][0]
```

If `messages` is empty after the wait, the mail did not arrive within 60 seconds.

### (b) Open a confirmation link

`links` holds the URLs found in the message. Pick the one you need and request it.

```ts
const msg = messages[0];
const link = msg.links.find((l: string) => l.includes('/confirm'));
expect(link).toBeTruthy();
await page.goto(link!); // or fetch(link!) for a server-side check
```

```python
link = next(l for l in messages[0]["links"] if "/confirm" in l)
requests.get(link, timeout=30)
```

### (c) Reply as the test recipient

Take the message id from the list, then reply. The reply goes from the address the mail was sent to, to the original sender. The recipient must be a verified destination address, otherwise the API returns `422`.

```sh
curl -X POST -H "Authorization: Bearer $MC_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"text": "Yes, I accept.", "quote": false}' \
  "https://<app hostname>/api/messages/<message id>/reply"
```

```ts
const r = await fetch(`${BASE}/api/messages/${msg.id}/reply`, {
  method: 'POST',
  headers: { Authorization: `Bearer ${KEY}`, 'Content-Type': 'application/json' },
  body: JSON.stringify({ text: 'Yes, I accept.', quote: false }),
});
expect(r.status).toBe(201);
```

```python
r = requests.post(
    f"{BASE}/api/messages/{msg['id']}/reply",
    headers={**headers, "Content-Type": "application/json"},
    json={"text": "Yes, I accept.", "quote": False},
    timeout=100,
)
assert r.status_code == 201
```

### (d) Clean up an address after a test

Delete the test address's mail (received and sent) so the next run starts clean.

```sh
curl -X DELETE -H "Authorization: Bearer $MC_API_KEY" \
  "https://<app hostname>/api/inboxes/<inbox id>/messages?to=b@gmail.com"
# {"deleted": 3}
```

```ts
await fetch(`${BASE}/api/inboxes/${INBOX}/messages?to=${encodeURIComponent('b@gmail.com')}`, {
  method: 'DELETE',
  headers: { Authorization: `Bearer ${KEY}` },
});
```

```python
requests.delete(
    f"{BASE}/api/inboxes/{INBOX}/messages",
    headers=headers,
    params={"to": "b@gmail.com"},
    timeout=30,
)
```

## Notes

- **Address formats.** Mail to `b@gmail.com` arrives at the catch-all address in the inbox's format, for example `b.gmail@<domain>` or `b=gmail.com@<domain>`. The `to` filter accepts either the catch-all address or the decoded real address (`b@gmail.com`), because both are stored with each message.
- **Outbound mail** (reply, forward) is delivered only to Cloudflare-verified destination addresses on the free plan. Other recipients return `422`.
- **Wait cap.** Cloudflare closes a proxied request after about 100 seconds without a response, so `wait` is capped at `90s` by the server. Set your HTTP client timeout above your `wait` value.
- **Relative URLs.** The `url` of an attachment is a path. Prefix it with the base URL.
- **Timestamps.** `date` and `received_at` are RFC 3339 with UTC times. `since` filters on `received_at`, not on `date`.
