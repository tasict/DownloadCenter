# Integration API (design)

Status: implemented in `daemon/internal/api` (tokens, REST, events, stream) and `daemon/internal/notify` (webhooks, channels, adapters, commands). This is the contract third-party developers build against: personal access tokens, a REST API, an event model, outbound webhooks, an event stream, a chat-command endpoint, and declarative notification adapters. The Download Station V4-compatible API is separate and exists only so that existing clients (Qget, Qfile, browser extensions) keep working.

All paths below are relative to `/<Name>/api/v1/` on the QTS admin port (8080/443). Requests and responses are JSON (UTF-8). Exposing the API outside the LAN (myQNAPcloud, a reverse proxy) is the user's choice; tokens must only be sent over HTTPS when it is.

## 1. Access tokens

### 1.1 Model

A token belongs to exactly one account that may use Download Center (the owner) and is created by that account in Settings › Access tokens. Who may use Download Center is QTS's application privilege (Control Panel › Privilege › Users › Edit Application Privilege); QTS administrators always may and are Download Center's administrators. It never grants more than its owner currently has:

```
effective rights = token scopes ∩ owner's current rights ∩ token restrictions
```

If the owner is deleted or disabled in QTS, or loses the Download Center application privilege, every token they own stops working (checked per request, cached for at most 30 s). Admin-only scopes, everyone's tasks and folder allowlists on a non-admin owner are rejected or dropped at creation and ignored at runtime, also when the owner stops being an administrator later; a token made by a regular user does not grow when its owner becomes one.

| Field | Meaning |
|---|---|
| `id` | Public id, `tok_` + 8 chars, shown in lists and logs |
| `name` | Owner-chosen label ("Home Assistant", "Discord bot") |
| `scopes` | See 1.2 |
| `tasks` | `own` (default) or `all` (admin owners only) |
| `folders` | Admin owners only: optional allowlist of save/move folders; adding a task elsewhere fails with `folder_not_allowed`. Regular users' tasks always go to their home `Download` folder |
| `sources` | Optional subset of `url`, `torrent`, `magnet` |
| `ip_allow` | Optional list of IPs/CIDRs |
| `expires_at` | 30 / 90 / 365 days or never |
| `rate_limit` | Requests per minute, default 120 |
| `last_used_at`, `last_used_ip`, `use_count` | Shown in the UI |

### 1.2 Scopes

| Scope | Allows |
|---|---|
| `tasks:read` | List and read tasks, files, peers, history |
| `tasks:add` | Add URLs, magnets, `.torrent` uploads |
| `tasks:control` | Start, pause, resume, reorder, select files, change per-task limits |
| `tasks:remove` | Remove tasks (keeps downloaded data) |
| `files:delete` | Remove tasks **and** delete downloaded data. Never implied by other scopes |
| `events:read` | Event stream and event history (only events the owner may see) |
| `notify:manage` | Manage the owner's own notification channels and webhooks |
| `stats:read` | Speeds, schedule state, disk space of allowed folders |
| `settings:read` | Read package settings (admin owner) |
| `settings:write` | Change package settings and schedule (admin owner) |

The UI offers presets: Read only (`tasks:read`, `stats:read`, `events:read`), Add download (read + `tasks:add`), Full control (no file deletion) (all task scopes except `files:delete`).

### 1.3 Format and storage

```
dct_<id>_<secret>      e.g. dct_k3x9q2mp_8bT1…（43 chars of base62）
```

Only `sha256(secret)` is stored. The full token is shown once at creation, with a copy button; afterwards only `dct_<id>_…` is displayed. Regenerating keeps the settings and issues a new secret.

### 1.4 Sending it

```
Authorization: Bearer dct_k3x9q2mp_8bT1…
```

Tokens are never accepted in the query string or in cookies (they would land in proxy and Apache logs). The one exception is the event stream, because browsers' `EventSource` cannot set headers: `POST /events/ticket` (with the bearer header) returns a single-use ticket valid for 30 s, which is passed as `?ticket=`.

Every token request is written to the audit log (time, token id, owner, IP, method, path, status). Failed authentication is rate-limited per IP.

## 2. REST API

Errors: HTTP status + `{"error": {"code": "folder_not_allowed", "message": "…"}}`. Messages are English unless the request sends `X-DC-Lang` with a QTS language code (`TCH`, `SCH`, `JPN`, `KOR`, `GER`, `FRE`, `SPA`, `ITA`, `POR`, `RUS`, `DUT`, `THA`); the codes never change.

| Method & path | Scope | Notes |
|---|---|---|
| `GET /me` | any | Owner, scopes, restrictions, expiry. Lets a client check what it can do |
| `GET /tasks?state=&kind=&q=&sort=&order=&after=&limit=` | `tasks:read` | Cursor pagination. `sort` = `queue` (default) \| `status` \| `progress` \| `eta` \| `elapsed` \| `name` \| `size` \| `created`; `order` = `asc` \| `desc`. A task waiting for a download slot has `queue_rank`: its place among the waiting tasks of its type (torrents, URLs, FTP), counting tasks the caller does not see |
| `GET /tasks/{id}` | `tasks:read` | `id` is the stable task hash (infohash or URL SHA-1). A private torrent has `"private": true` (here only, not in the list) |
| `POST /tasks/check` | `tasks:add` | `{"sources": [...]}` (or the .torrent files as multipart `file`): per source `status` `new`, `in_list`, `same_torrent`, `same_content` (same files, another infohash; adding it with `"content_of": "<task_id>"` makes it another source of that task) or `downloaded`, with `task_id` for your own tasks and `"private": true` when the source or the matching task is a private torrent. Private torrents are never another source: `content_of` then fails with `409 private_torrent` |
| `GET /tasks/{id}/files` | `tasks:read` | |
| `GET /tasks/{id}/folder` | `tasks:read` | `{"path": "Download/…", "file": "…"}`: the folder the task's data is in right now (its temporary folder while it downloads) and, for a single file, the file to select |
| `GET /folders?path=` | `tasks:add` | Folders to save into: the shared folders when `path` is empty, otherwise the sub-folders of `path`, as `{"folders": [{"name", "path", "writable", "choosable", "free"}], "free": bytes, "writable", "choosable"}` (`choosable` = can hold downloads; `free` per entry only at the top level). A token with a folder allowlist sees only those folders and what lies below them (`403 folder_not_allowed` elsewhere). `free_only=1` answers `{"free": bytes}` alone. Regular users get their home `Download` folder only |
| `POST /tasks` | `tasks:add` | `{"source": "<url or magnet>", "folder": "/Download", "move_to": null, "files": "all"\|[indices], "auto_remove": null\|"completed"\|"seeded", "start": true}`. A source that duplicates an existing task returns `409 duplicate` with that task's id, unless it is the same torrent, which is merged (`200`, `"merged": true`; a private torrent is never merged and returns `409 duplicate`); multiple sources via `"sources": [...]`. For regular users `folder` and `move_to` must be omitted (or equal their home `Download`), otherwise `folder_not_allowed`. A folder on a read-only volume fails with `folder_read_only` |
| `POST /tasks/torrent` | `tasks:add` | `multipart/form-data`, field `file`, plus the same options |
| `POST /tasks/{id}/pause` · `/resume` · `/retry` | `tasks:control` | |
| `PATCH /tasks/{id}` | `tasks:control` | Any of `{"position": "top"\|"up"\|"down"\|"bottom"\|n\|{"before": id}\|{"after": id}, "files": [...], "max_download": bytes_per_s, "max_upload": bytes_per_s, "sequential": bool, "auto_remove": ""\|"completed"\|"seeded", "proxy": "auto"\|"none"\|profile id}` (`proxy` for URL tasks only). `n` counts every task in the queue, including ones the caller does not see; `{"before": id}` / `{"after": id}` puts the task next to a task the caller sees (`404 not_found` otherwise, `400 bad_request` when it is the task itself) and the answer adds `started` and `stopped`: the tasks (`{"id", "name"}`, only ones the caller sees) that got or lost a download slot by the move |
| `POST /tasks/bulk` | `tasks:control` (`tasks:remove` for `remove`) | `{"ids": [...]\|"all", "action": "pause"\|"resume"\|"retry"\|"top"\|"up"\|"down"\|"bottom"\|"move"\|"remove"}`, answers `count`. `move` needs `"before": id` or `"after": id` and moves the tasks as one block, in their queue order, next to that task; like the anchored `position` it answers `started` and `stopped`. `remove` takes `delete_files` and `"completed": true` (only finished tasks) |
| `DELETE /tasks/{id}?delete_files=false` | `tasks:remove` (+ `files:delete` when `true`) | |
| `GET /stats` | `stats:read` | Current speeds, schedule mode and next change, free space per allowed folder |
| `GET /events?after=<event id>&limit=` | `events:read` | Polling alternative to the stream |
| `POST /events/ticket`, `GET /events/stream?ticket=` | `events:read` | Server-Sent Events, one `data:` line per event, heartbeat every 25 s |
| `POST /commands` | per command | See 5 |
| `GET/POST/PATCH/DELETE /webhooks…` | `notify:manage` | See 4 |
| `GET/PUT /settings`, `GET/PUT /schedule` | `settings:*` | `GET /settings` also answers `engine_versions` and, with a torrent engine, `bt_identity` (`peer_id`, `user_agent`): what the default client identity sends |

## 3. Events

The engine tick writes events into an append-only table (kept 30 days). Every consumer — built-in channels, webhooks, the stream, polling — reads the same events, filtered to what the subscriber's owner may see.

```json
{
  "id": 18342,
  "type": "task.completed",
  "time": "2026-10-01T22:41:07+08:00",
  "owner": "alice",
  "task": {
    "id": "c9e1…", "name": "debian-13.1.0-amd64-netinst.iso", "kind": "url",
    "size": 663748608, "folder": "/Download", "duration_s": 212
  }
}
```

| Type | When |
|---|---|
| `task.added` | A task was created (any client) |
| `task.started` | Left the queue and began transferring |
| `task.paused`, `task.resumed` | Paused or resumed by a person or for a reason; `by` is `user`, `chat`, `v4`, `timer` (a timed pause ended) or `disk` (low space). Schedule switches report `schedule.changed` instead |
| `task.completed` | All selected data downloaded and moved out of the temporary folder (torrents keep seeding from there) |
| `task.seeding_finished` | Ratio or seed-time target reached |
| `task.moved` | Moved to its completion folder |
| `task.failed` | `error.code`, `error.message`, `retryable` |
| `task.removed` | `data_deleted: true/false`, `auto: true` when removed by the auto-remove option |
| `task.merged` | Another magnet/.torrent of the same content was folded into this task (`source` added) |
| `task.source_switched` | A content-merged task moved to another torrent of the same files (`from`, `to`) |
| `account.expiring` | A file-hosting account expires within 7 days or ran out of today's traffic (owner only) |
| `queue.idle` | Nothing left downloading |
| `disk.low` | A download folder's volume fell below the configured threshold; downloads to it pause |
| `schedule.changed` | The schedule switched the mode (`full`, `limited`, `off`), with `by`: `schedule` when the time came, `settings` when the schedule was edited |
| `engine.down`, `engine.up` | An engine sidecar (dc-dl or dc-bt) stopped answering / is back (admin only) |
| `security.token_created`, `security.token_rejected` | Admin only |
| `notify.disabled` | A channel or webhook was disabled after 20 failed deliveries in a row; always delivered to its owner's other channels |

## 4. Outbound webhooks

Created per owner (`notify:manage`). Each has a URL, the event types it wants, and a secret. Left empty, the secret is generated and returned once when the webhook is created (the web UI shows it then).

```
POST <your URL>
Content-Type: application/json
User-Agent: DownloadCenter/1.0
X-DC-Event: task.completed
X-DC-Delivery: 7f3c…                 (unique per attempt group; use it to de-duplicate)
X-DC-Signature: t=1790866032,v1=<hex HMAC-SHA256(secret, t + "." + body)>
```

Verify by recomputing the HMAC and rejecting timestamps older than 5 minutes. Delivery: 10 s timeout, success = any 2xx, retries after 1, 5, 30 min and 2, 6 h, then the delivery is marked failed; 20 consecutive failures disable the webhook and notify its owner. The UI shows the last 50 deliveries with status and response time, and has "Send test notification".

Target restrictions: non-admin owners cannot target loopback, link-local, or the NAS's own addresses (prevents using webhooks to reach dcd, QTS or other local services). Administrators' webhooks are not restricted.

## 5. Chat commands

`POST /commands` takes the text a person typed in a chat and returns a reply, so a bot for any chat service is a thin forwarder. Replies, like the notification texts of the built-in channels, are in Traditional Chinese for now; `locale` is accepted but not used yet.

```json
→ {"text": "/add magnet:?xt=urn:btih:…", "locale": "zh-TW"}
← {"ok": true, "reply": "已加入：Big Buck Bunny（等待中，第 2 位）", "task": {"id": "dd82…"}, "buttons": [{"label": "暫停", "command": "/pause dd82"}]}
```

| Command | Scope |
|---|---|
| `/list [下載中\|已完成\|錯誤]` | `tasks:read` (numbered list; numbers stay valid for 10 minutes) |
| `/add <url or magnet>` (several lines allowed) | `tasks:add` |
| `/pause <n\|all>`, `/resume <n\|all>`, `/retry <n>` | `tasks:control` |
| `/del <n>` | `tasks:remove`, asks for `/del <n> confirm` |
| `/speed`, `/disk` | `stats:read` |
| `/limit <2M\|off>`, `/sched on\|off` | `settings:write` |
| `/help` | any |

`buttons` lets adapters that support them (Telegram inline keyboards, LINE quick replies, Discord components) render actions; plain-text services ignore them.

### 5.1 Operating from a channel

Notification and chat control are the same channel. Each channel has an `operate` switch, off by default and only available on services that can deliver incoming messages: Telegram, and LINE when the NAS is reachable over public HTTPS (the address is set by an administrator in Settings › Notifications & integrations › Address from outside). With it off, the bot only sends notifications and ignores commands. Webhook channels have no switch: your service operates downloads by calling `/commands` with a token.

### 5.2 Linking a chat account

Channels with `operate` on map a chat user to a QTS account by pairing, per channel: the user clicks "Link my account" in the UI, gets a 6-digit code valid for 10 minutes, and sends `/link 482913` to the bot. The chat user id is then bound to that QTS account with the scopes chosen at pairing time. Messages from unlinked chat users get only the `/link` instruction.

## 6. Built-in channels

| Channel | Notify | Two-way | Needs |
|---|---|---|---|
| Telegram | ✓ | ✓ (long polling, no public address needed; sending a `.torrent` file adds it) | Bot token from @BotFather |
| Discord | ✓ (webhook, embeds) | Later: interactions need a public HTTPS endpoint | Channel webhook URL |
| LINE | ✓ (Messaging API push) | ✓ only with a public HTTPS webhook | Messaging API channel access token + user/group id. LINE Notify was discontinued in 2025, so it is not supported |
| Slack | ✓ (incoming webhook) | — | Webhook URL |
| ntfy / Gotify / Bark | ✓ | — | Server URL + topic/key |
| QTS Notification Center | ✓ (email, SMS, push via QTS) | — | Nothing |
| Generic webhook | ✓ | via `/commands` | See 4 |

Per channel: event selection (none chosen means no notifications), the `operate` switch, own tasks vs all (admins), quiet hours, and digest mode (one summary every N minutes instead of one message per event). Through the API a channel can also have a message `template` for the text of chat services; webhooks and declarative adapters send their own format.

## 7. Declarative adapters

New services can be added without code: an adapter is a JSON manifest describing one HTTP request per event, imported in Settings › Notifications & integrations. Manifests cannot run code, read files, or reach addresses that webhooks cannot reach.

```json
{
  "adapter": "mattermost",
  "title": "Mattermost",
  "fields": [{"key": "url", "label": "Incoming webhook URL", "type": "url", "secret": true}],
  "request": {
    "method": "POST",
    "url": "{{fields.url}}",
    "headers": {"Content-Type": "application/json"},
    "body": {"text": "{{event.title}}\n{{task.name}}（{{task.size_h}}）"}
  }
}
```

Template variables: `event.*`, `task.*` (with `_h` human-readable variants such as `size_h`, `duration_h`), `owner`, `nas.name`, `ui_url`. Values are JSON-escaped when substituted into `body` and percent-encoded inside `url`.

## 8. AI agents

AI agents (Claude Code, Codex, Gemini CLI …) use this API through the skill file `docs/skill/SKILL.md`, which every installation serves at `/<Name>/docs/skill/SKILL.md`. The Access tokens page has an AI Agent section that creates a token with the Full control (no file deletion) preset and shows a setup command that installs the skill and stores the address and token in `~/.config/download-center/config`. See [AI-AGENT.md](AI-AGENT.md). The skill tells agents to treat everything the API returns as data, never as instructions, and to confirm removals with the user.

## 9. Implementation notes

- Tokens, channels, webhooks, pairings, events and deliveries are SQLite tables beside the task tables; secrets (bot tokens, webhook secrets) are stored in `data/` with 0600 and never returned by the API after creation.
- Delivery, retries, digests and the Telegram long poll run as goroutines inside `dcd` (`internal/notify`), supervised with the daemon by the watchdog, not in Apache requests.
- Outbound HTTP uses Go clients from `internal/netutil` with explicit timeouts; for non-admin owners the target is resolved and forbidden ranges are refused after resolution, at dial time or (through a proxy) before the request (DNS rebinding, redirects).
