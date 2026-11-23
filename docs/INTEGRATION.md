# Integration API (design)

Status: implemented in `daemon/internal/api` (tokens, REST, events, stream) and `daemon/internal/notify` (webhooks, channels, adapters, commands). This is the contract third-party developers build against: personal access tokens, a REST API, an event model, outbound webhooks, an event stream, a chat-command endpoint, and declarative notification adapters. The Download Station V4-compatible API is separate and exists only so that existing clients (Qget, Qfile, browser extensions) keep working; for scripts written against it, it also answers two additions of its own: `Task/AddTracker` (repeatable `hash` and `url`, `public=1` for the public tracker list; answers `added` and `skipped`, the tasks that got trackers and those that could not take them) and `bt.public_trackers` (0 or 1) in `Config/Get` and `Config/Set`.

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
| `search:use` | Search for torrents and list the search sources (see 2.1). Never implied by other scopes |
| `settings:read` | Read package settings (admin owner) |
| `settings:write` | Change package settings and schedule (admin owner) |

The UI offers presets: Read only (`tasks:read`, `stats:read`, `events:read`), Add download (read + `tasks:add`), Full control (no file deletion) (all task scopes except `files:delete`). No preset has `search:use`: tick Search for torrents among the custom permissions.

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

Errors: HTTP status + `{"error": {"code": "folder_not_allowed", "message": "…"}}`. Messages are English unless the request sends `X-DC-Lang` with a QTS language code (`TCH`, `SCH`, `JPN`, `KOR`, `GER`, `FRE`, `SPA`, `ITA`, `POR`, `RUS`, `DUT`, `THA`, `HUN`); the codes never change.

**Limits and guards for every caller.**

- A multipart request (`POST /tasks/torrent`, and `POST /tasks/check` or `POST /tasks/probe` with files) may be at most 32 MiB: `413 too_large`. Of each `.torrent` file only the first 16 MiB are read, so a larger file fails as an invalid torrent; the same 16 MiB limit applies to `.torrent` links, V4 uploads and files sent to a Telegram channel.
- An account looks up at most 3 magnet links at once (`POST /tasks/probe`): `429 probe_limit`; ask again when one has finished.
- Tasks added with a token, from a chat channel or by an AI agent through the page's WebMCP tools (requests with `X-DC-Via: webmcp`) never connect to the NAS's own, loopback or link-local addresses (for torrents: trackers, web seeds and peers), whoever the account is, administrators included; neither does such a magnet look-up. Regular users' tasks are always limited this way. Only what an administrator adds with a QTS session is not: in the web UI, with the Deskpane widget, or from a Download Station client (V4).
- A token limited to some kinds of sources cannot add a link that turns out to be a `.torrent` file unless torrent files are among them: `403 source_not_allowed`.
- A token that sees only its owner's tasks gets `409 duplicate_other_owner` for a torrent that another account is downloading, as a regular user does, even when its owner is an administrator.
- A password inside a task's address is never shown: `source`, and the addresses in a task's merged `sources`, carry it as `***` (`ftp://user:%2A%2A%2A@host/…`), so such a task cannot be added again from that value.

| Method & path | Scope | Notes |
|---|---|---|
| `GET /me` | any | Owner, scopes, restrictions, expiry. Lets a client check what it can do |
| `GET /tasks?state=&kind=&q=&sort=&order=&after=&limit=` | `tasks:read` | Cursor pagination. `sort` = `queue` (default) \| `status` \| `progress` \| `eta` \| `elapsed` \| `name` \| `size` \| `created`; `order` = `asc` \| `desc`. A task waiting for a download slot has `queue_rank`: its place among the waiting tasks of its type (torrents, URLs, FTP), counting tasks the caller does not see. A `seeding` torrent that waits for a seeding slot (`torrent.max_seeding`) has `seed_waiting: true` and uploads nothing until it gets one |
| `GET /tasks/{id}` | `tasks:read` | `id` is the stable task hash (infohash or URL SHA-1). A private torrent has `"private": true` (here only, not in the list). Next to the task the answer has `trackers` (added by hand or merged from another link) and `public_trackers` (the public tracker list as it was added); a torrent's task also has `tracker_count` (its own, extra and public trackers) and `public_missing` (trackers of the current public list it does not have yet). A torrent that is not complete also has `health`, what its source health is based on (see the next row; `sources` stays the list of merged links): `level` (as `source_health`, absent for a failed task), `reason` (`unregistered`, `no_seeders` or `incomplete` for `no_sharing` and `likely_dead`), `waiting_secs` (running without any connection or progress, counted while the task runs only and kept across restarts), `incomplete_secs` (connected, but the peers together lack pieces and nothing comes in), `running` (the times grow now), `no_sharing_secs` and `likely_dead_secs` (the thresholds), `ever_connected`, `last_connected_at`, `last_data_at`, `availability` (complete copies among the connected peers, 0.82 = 82% of the pieces; -1 without the file list), `last_seen_complete` (Unix time, 0 unknown), `dht` (not for private torrents: `available`, whether peers can be looked for on the network now; `searched_secs` of the waiting time; `found`, peers DHT found, not counting the NAS itself, 0 = nobody found) and `trackers` (each with `url`, `host`, `state` `working`, `failing`, `unregistered` (the tracker says the torrent does not exist) or `not_contacted`, `seeds` and `leechers` (-1 unknown; leechers include the NAS itself once it announced), `message` (the tracker's reason when it fails) and `public` (from the public list)). Reading it asks the engine about the trackers; the list does not |
| (torrents, list and detail) | | Every torrent task has `peers_known` (peers found, connected or not, without the NAS itself, which DHT and trackers hand back too), `no_peers_since` (running without any connection or progress since then, Unix time; 0 when not; a connection counts once it lasted a minute, since the NAS itself and fake peers are connected for moments), `peer_searches` (how often Download Center looked for peers again since then: after 15 minutes, then after 30, 60 and every 120 minutes; meanwhile the task leaves its download slot to the next torrent) and `metadata_since` (a magnet link getting its file list since then; 0 once it has it). A torrent that is not complete has `source_health`: `ok` (connected, or data came in lately), `searching` (no source for 10 minutes or more, not enough to say more), `no_sharing` (no source for `torrent.no_sharing_hours`, 6 by default, while no tracker reports seeders and DHT found nobody; seeders count when the torrent's own or added trackers report them, or two trackers of the public list do, since some of those report a seeder for any torrent; or the connected peers have lacked pieces that long) or `likely_dead` (no source for `torrent.likely_dead_days`, 3 by default, without any tracker reporting seeders, or a tracker says the torrent does not exist). These are judgments, never acted on: Download Center does not delete, pause or move such a task. URL tasks have none of these |
| `POST /tasks/{id}/find-peers` | `tasks:control` | Makes a torrent without a connection forget the peers it could not reach and ask DHT and its trackers again now, instead of at its next automatic search. Answers `{"refreshed": true}`, or `{"refreshed": false, "reason": …}`: `connected` (it has a connection; nothing is interrupted), `not_running` (paused, waiting for a slot or failed), `too_soon` with `retry_after` (seconds; once a minute per task, automatic searches included). `400 unsupported` for URL tasks |
| `POST /tasks/{id}/trackers` | `tasks:control` | `{"trackers": ["udp://…", "https://…"], "public": true}` (at least one of the two; up to 200 trackers): adds trackers to a torrent, and with `public` the public tracker list; the engine announces to them right away. A torrent that gets the public list connects to no loopback or NAS address from then on, whoever owns it. Trackers the task has are left out. Answers `added` (new trackers given) and `public_added`. Errors: `400 tracker_invalid` (not an `http`, `https` or `udp` URL with a host, `udp` with a port), `400 tracker_not_allowed` (for every caller except an administrator with a QTS session, that is regular users, tokens, chats and AI agents through WebMCP: a loopback, link-local or NAS address written out, also as `2130706433` or `0x7f.1`, or `localhost`; names are checked when dc-bt connects), `400 too_many_trackers` (a task takes up to 200 trackers besides its own and the public list), `409 private_torrent` (private torrents only use their own trackers), `400 unsupported` (URL tasks) |
| `GET /trackers/public` | `tasks:read` | The public tracker list: `trackers`, `count`, `source` (`feed`: from the Download Center website, `builtin`: the list built into this version), `updated_at`, `checked_at`, `error`. Reading it fetches a list more than a day old again, in the background |
| `POST /trackers/public/refresh` | `settings:write`, administrators | Fetches the list now; answers the same, with `error` when that failed (the list in use stays) |
| `POST /tasks/check` | `tasks:add` | `{"sources": [...]}` (or the .torrent files as multipart `file`): per source `status` `new`, `in_list`, `same_torrent`, `same_content` (same files, another infohash; adding it with `"content_of": "<task_id>"` makes it another source of that task) or `downloaded`, with `task_id` for your own tasks and `"private": true` when the source or the matching task is a private torrent. Private torrents are never another source: `content_of` then fails with `409 private_torrent` |
| `POST /tasks/probe` | `tasks:add` | The file list of a magnet link before adding it: `{"magnet": "magnet:?…", "folder": "Download", "move_to": "Public"}` answers `{"state": "pending"}` while the metadata is fetched (ask again every few seconds) and then `{"state": "ready", "infohash", "torrent": {"name", "files": [{"index", "path", "size"}]}, "free"}`; `{"magnet", "cancel": true}` stops it. A `.torrent` as multipart `file` (with the same `folder` and `move_to` fields) answers `ready` at once. `free` is the free space of the volume the data will be on: that of `move_to`, else of the default move folder, else of `folder` (`move_to: ""` means not moving). `429 probe_limit`, `413 too_large`, `409 duplicate_other_owner` |
| `GET /tasks/{id}/files` | `tasks:read` | `{"files": [{"index", "path", "size", "done", "priority"}]}`; `priority` 0 = not downloaded, 1–7 = downloaded, higher first (the UI's High, Medium and Low are 7, 4 and 1). A torrent with several files to download starts with its largest video at 7 and the other files at 4 |
| `GET /tasks/{id}/folder` | `tasks:read` | `{"path": "Download/…", "file": "…"}`: the folder the task's data is in right now (its temporary folder while it downloads) and, for a single file, the file to select |
| `GET /folders?path=` | `tasks:add` | Folders to save into: the shared folders when `path` is empty, otherwise the sub-folders of `path`, as `{"folders": [{"name", "path", "writable", "choosable", "free"}], "free": bytes, "writable", "choosable"}` (`choosable` = can hold downloads; `free` per entry only at the top level). A token with a folder allowlist sees only those folders and what lies below them (`403 folder_not_allowed` elsewhere). `free_only=1` answers `{"free": bytes}` alone. Regular users get their home `Download` folder only |
| `POST /tasks` | `tasks:add` | `{"source": "<url or magnet>", "folder": "/Download", "move_to": null, "files": "all"\|[indices], "auto_remove": null\|"completed"\|"seeded", "start": true}`. A source that duplicates an existing task returns `409 duplicate` with that task's id, unless it is the same torrent, which is merged (`200`, `"merged": true`; a private torrent is never merged and returns `409 duplicate`); multiple sources via `"sources": [...]`. For regular users `folder` and `move_to` must be omitted (or equal their home `Download`), otherwise `folder_not_allowed`. A folder on a read-only volume fails with `folder_read_only` |
| `POST /tasks/torrent` | `tasks:add` | `multipart/form-data`, field `file`, plus the same options; each file at most 16 MiB, at most 32 MiB per request (`413 too_large`) |
| `POST /tasks/{id}/pause` · `/resume` · `/retry` | `tasks:control` | |
| `PATCH /tasks/{id}` | `tasks:control` | Any of `{"position": "top"\|"up"\|"down"\|"bottom"\|n\|{"before": id}\|{"after": id}, "files": [indices]\|{"<index>": 0..7}, "max_download": bytes_per_s, "max_upload": bytes_per_s, "sequential": bool, "auto_remove": ""\|"completed"\|"seeded", "proxy": "auto"\|"none"\|profile id}` (`proxy` for URL tasks only). `n` counts every task in the queue, including ones the caller does not see; `{"before": id}` / `{"after": id}` puts the task next to a task the caller sees (`404 not_found` otherwise, `400 bad_request` when it is the task itself) and the answer adds `started` and `stopped`: the tasks (`{"id", "name"}`, only ones the caller sees) that got or lost a download slot by the move |
| `POST /tasks/bulk` | `tasks:control` (`tasks:remove` for `remove`) | `{"ids": [...]\|"all", "action": "pause"\|"resume"\|"retry"\|"top"\|"up"\|"down"\|"bottom"\|"move"\|"remove"\|"add_public_trackers"}`, answers `count`. `add_public_trackers` gives every torrent the public tracker list; URL tasks, private torrents and those that have the whole list are skipped and counted in `skipped`. `move` needs `"before": id` or `"after": id` and moves the tasks as one block, in their queue order, next to that task; like the anchored `position` it answers `started` and `stopped`. `remove` takes `delete_files` and `"completed": true` (only finished tasks) |
| `DELETE /tasks/{id}?delete_files=false` | `tasks:remove` (+ `files:delete` when `true`) | |
| `GET /stats` | `stats:read` | Current speeds, schedule mode and next change, free space per allowed folder |
| `GET /events?after=<event id>&limit=` | `events:read` | Polling alternative to the stream |
| `POST /events/ticket`, `GET /events/stream?ticket=` | `events:read` | Server-Sent Events, one `data:` line per event, heartbeat every 25 s. The caller is checked again every 2 minutes: the stream ends when its session or token is gone or what it may see has changed (role, scopes, use of Download Center), and a new ticket is needed |
| `POST /commands` | per command | See 5 |
| `GET/POST/PATCH/DELETE /webhooks…` | `notify:manage` | See 4 |
| `GET/PUT /settings`, `GET/PUT /schedule` | `settings:*` | `GET /settings` also answers `engine_versions`, `memory_profile` (`low` on a NAS with 1.5 GB of memory or less, `normal` otherwise; read-only, decided when Download Center starts) and, with a torrent engine, `bt_identity` (`peer_id`, `user_agent`): what the default client identity sends, and `public_tracker_list` (`count`, `source`, `updated_at`, `checked_at`, `error`). `torrent.public_trackers` (default `false`) gives every new torrent and magnet link that is not private the public tracker list. `torrent.no_sharing_hours` (1–168, default 6) and `torrent.likely_dead_days` (1–30, default 3) set when a torrent without sources counts as `no_sharing` and `likely_dead`; values out of range are moved into it, and days shorter than the hours count as the hours. `torrent.max_seeding` (0–50, `0` = no limit) is how many torrents seed at once: the others wait in list order (their tasks have `seed_waiting: true`), and a torrent that has not uploaded anything for 30 minutes keeps seeding without counting. Defaults: `bt.max_num` 5 and `torrent.max_conn` 300, or 2 and 100 when `memory_profile` is `low` and the settings were never saved; `torrent.max_seeding` 0, or 3 on a new installation whose `memory_profile` is `low` |
| `GET /search/sources`, `POST /search`, `GET /search/{id}`, `DELETE /search/{id}`, `GET /search/{id}/torrent/{ref}` | `search:use` | See 2.1 |
| `/search/plugins…` | web UI, administrators | See 2.1 |

`GET /me` also answers `search` (`{"available": true}` when at least one search source is enabled and set up and the caller may use it), for every caller.

### 2.1 Torrent search

A search asks every enabled search source at once and collects what they find. Sources are plugins: built-in ones for freely distributable content (Internet Archive, Kiwix, Fedora, Arch Linux, FOSS Torrents), Torznab servers such as Jackett or Prowlarr, and plugin files an administrator adds. A plugin file is a declarative description (one GET to a fixed host, and where the fields of each result are in the answer); it cannot run code. An administrator can also install search plugins written for Synology's and QNAP's download managers: those are programs, which run on the NAS in a restricted environment after the administrator agrees to the terms, and reach the internet only through a stricter relay ([SEARCH-PLUGINS.md](SEARCH-PLUGINS.md) section 9). Every request to a source other than those is made by the NAS through the proxy a regular user's download of that address would use, at most 15 s, 2 MiB and 3 redirects (http and https only), with the User-Agent `DownloadCenter/<version> (+https://github.com/tasict/DownloadCenter)` and nothing of the caller's (no cookie, session, token or user name). It never reaches a loopback, link-local, unspecified or multicast address or one of the NAS's own, whoever searches and through any redirect; a Torznab server on the NAS itself cannot be used. Nothing is asked of a source unless someone searches, tests it or installs a plugin from its address. Search sources are third-party sites: download only what you have the right to download.

**Searching.** `POST /search` with `{"q": "big buck bunny", "category": "all", "sources": ["kiwix"]}` (`q` 2 to 200 characters; `category` `all` (default), `video`, `audio`, `software`, `books` or `other`; `sources` optional, the ids of `GET /search/sources`, default every one that has the category) answers `202 {"id": "…"}`. Poll `GET /search/{id}` (the UI does every 600 ms) until `done` is `true`:

```json
{
  "id": "112f706d57f46ae133747174", "q": "big buck bunny", "category": "all", "done": false,
  "sources": [{"id": "internet-archive", "name": "Internet Archive", "state": "done", "count": 12},
              {"id": "jackett", "name": "Jackett", "state": "failed", "count": 0, "error": "The server refused the API key"}],
  "results": [{"title": "Big Buck Bunny", "size": 276445467, "seeders": 30, "leechers": 2, "date": 1717401600,
               "category": "video", "sources": ["jackett"], "page": "https://…", "infohash": "dd82…",
               "magnet": "magnet:?xt=urn:btih:dd82…&dn=Big+Buck+Bunny", "ref": "9f2c…"}],
  "total": 1
}
```

A source's `state` is `running`, `done` (with `count`), `failed` (with `error`, one of a fixed set of reasons: timed out, could not connect, the server refused the API key, could not read the answer, the address is not allowed, the source is busy, the answer is too large, an HTTP status, a Torznab server's own error, or for a plugin from another NAS: the plugin failed, the plugin did not finish in time, this NAS cannot run this plugin with the reason) or `cancelled`. Results arrive source by source; the same torrent (same infohash) from several sources is one result listing them all, with the highest counts.

Result fields, each left out when unknown: `title` (no control characters, at most 300), `size` (bytes), `seeders`, `leechers`, `webseed` (the source's torrents download from the site itself even without seeders), `date` (Unix time), `category`, `sources`, `page` (http(s) only; left out when it contains one of the source's secret values or is on the host of the administrator's Torznab server), `infohash` (40 hex digits), `magnet` (built by Download Center from the infohash, the title and at most 20 http/https/udp trackers, never passed through from a source; trackers that are `localhost` or a literal local or NAS address are left out), `torrent_url` (the source's `.torrent` address, only when it carries none of the source's keys and is not on the administrator's Torznab server) and `ref`.

**Adding a result.** Searching never adds anything; adding goes through `POST /tasks` with all its rules (it needs `tasks:add`). Use the `magnet` when there is one. Otherwise `GET /search/{id}/torrent/{ref}` fetches the `.torrent` through the NAS without the plugin's headers (a site that needs them to hand out `.torrent` files only works through its magnet links). The NAS fetches it only from public addresses on ports 80 and 443, except from the host of the administrator's Torznab server, which keeps the usual rule on any port. It answers `application/x-bittorrent` (pass it on to `POST /tasks/torrent`) or, when the source sent a magnet link instead, `{"magnet": "magnet:?…"}`. Every failure reads "This source did not give a valid .torrent file" (`502 search_torrent_failed`); an account fetches at most two at a time (`429 rate_limited`); `404 not_found` when the search has ended, or its source was switched off or is no longer offered to the account. Sources whose torrents rely on web seeds (Internet Archive, Kiwix) give no magnet link when they have a `.torrent`: a magnet link of such a torrent would never get its file list.

**Ownership and limits.** A search belongs to the account that started it (a token's owner); any other account, administrators included, gets `404 not_found` for it and its `ref`s. Searches live in memory only, for 30 minutes after they finish and at most 3 per account; a new search cancels the account's search still running, `DELETE /search/{id}` cancels one. 20 searches per account per minute. Keywords are not logged or stored.

`GET /search/sources` lists the sources that can be searched now: `{"sources": [{"id", "name", "builtin", "categories"}]}`; addresses and settings are not shown.

**Who can use a source.** An administrator makes each source available to everyone or to administrators only. Built-in sources and plugin files without a secret setting start for everyone; Torznab servers, plugins with a secret setting and plugins from another NAS start for administrators only, because their results can carry what identifies the account on the site. A source for administrators only is left out of `GET /search/sources` and `search.available` for other accounts, and naming it in `POST /search` is the same as naming an unknown source (`409 search_no_sources` when nothing is left). A token counts as its owner: an administrator's token sees the sources for administrators.

**Managing sources** (administrators in the web UI only: tokens get `403 session_required`, regular users `403 admin_only`):

| Method & path | Notes |
|---|---|
| `GET /search/plugins` | `{"sources": [...], "limit": 50}`, each with `id`, `name`, `version`, `description`, `homepage`, `type` (`json`, `rss`, `torznab`, `dlm`, `qnap-addon`), `kind` (`builtin`, `custom`, `torznab`, `dlm`, `qnap-addon`), `builtin`, `enabled`, `needs_setup` (a required setting is empty; such a source is not searched), `host` (where it connects; for a plugin from another NAS the hosts it declares), `headers` (names of the headers it sends), `settings` (`id`, `label`, `secret`, `required`, `set`, and `value` for settings that are not secret; a secret's value is never returned), `categories`, `match`, `webseed`, `test_query`, `audience` (`all` or `admins`), `secret_bearing` (a secret setting has a value), `runs_code`; plugins from another NAS also have `site` (what they declare), `consent` (`user`, `time`: who agreed to the terms and when, Unix time), `key_fingerprint` (add-ons) and, when this NAS cannot run them now, `runtime` (`ok`: false, `code`, `reason`). The answer also has `runtime` (`ok`, `code`, `reason`): whether this NAS can run plugins from another NAS (`code` `php_missing`, `php_broken`, `no_isolation`, `check_failed` or `unchecked`) |
| `POST /search/plugins/inspect` with a file | `{"file": "<Base64>", "filename": "x.dlm"}` (`.dlm` or `.addon`, at most 2 MiB): checks a plugin from another NAS without saving or running it and answers `id`, `kind`, `runs_code` (true), `sha256`, `name`, `version`, `description`, `site`, `account` (it signs in), `key_fingerprint` (add-ons), `key_changed` (an installed add-on with this id has another key), `exists` and `runtime` (`ok`, `code`, `reason`; answered with 200 even when this NAS cannot run it) |
| `POST /search/plugins` with a file | `{"file", "filename", "accept_risk": true, "sha256": "<from inspect>"}`, `"replace": true` for a new version of an installed one: installs a plugin from another NAS, disabled and for administrators only. Never from an address. `accept_risk` is the administrator's agreement to the terms shown in the UI, bound to that exact file; it is recorded with the user and time |
| `POST /search/plugins/inspect` | `{"json": "<plugin file>"}` or `{"url": "https://…"}`: checks a plugin without saving anything and answers `id`, `name`, `version`, `description`, `homepage`, `type`, `match`, `hosts`, `headers`, `settings`, `categories`, `exists` (a custom plugin with that id is installed), `keeps_settings` (installing it with `replace` keeps the installed one's state, audience and saved values, see below) and `sha256` (of the file checked; send it back when installing from an address). `409 search_plugin_exists` when the id belongs to a plugin from another NAS |
| `POST /search/plugins` | Installs `{"json": …}` or `{"url": …, "sha256": "<from inspect>"}` (disabled until switched on, whatever its type; from an address `sha256` is required (`400 invalid_request` without it), and a file other than the one inspected answers `409 search_plugin_changed` and installs nothing; `"replace": true` puts a new version in place of an installed one; it keeps the installed one's on/off state, audience and saved values only with the same type, the same full request address (scheme, host, port, path and parameters), the same link fields (how magnet, `.torrent` and page addresses are built) and the same settings with the same secret flags, otherwise it starts switched off with the default audience; a plugin file and a plugin from another NAS cannot replace each other (`409 search_plugin_exists`); `"settings": {…}` fills them in), or adds a Torznab server `{"torznab": {"name", "url", "apikey"}}` (enabled; `url` is the server's Torznab address, http or https, without the key, for example `http://192.168.1.20:9117/api/v2.0/indexers/all/results/torznab`). Answers the source as listed |
| `POST /search/plugins/test` | Same body as installing (`{"json"}`, `{"url"}` or `{"torznab"}`, with `settings`): asks the source once without saving anything. An id that is installed or built in is fine; a plugin whose id is installed is tested with the installed source's settings unless `settings` are given, and only when the definition would keep them on replacing (same type, address and settings) |
| `PUT /search/plugins/{id}` | `{"enabled": true, "settings": {"apikey": ""}, "name": "…", "audience": "all"}`, any of them; an empty secret keeps the saved one, `name` for Torznab sources only, `audience` `all` or `admins` for every source |
| `DELETE /search/plugins/{id}` | Custom and Torznab sources and plugins from another NAS (with their files) |
| `POST /search/plugins/{id}/verify` | `{}`: asks a plugin from another NAS that signs in whether the site takes its saved account: `{"ok": true}` or `{"ok": false, "error": "…"}` (`error` absent when the site refused the account). `400 invalid_request` for a source without an account |
| `POST /search/plugins/{id}/test` | Asks the source once with its test keyword: `{"ok": true, "count": 12, "ms": 840}` or `{"ok": false, "error": "…"}` |
| `GET /search/plugins/{id}` | The source's definition as a file (`<id>.json`), without setting values; `400 invalid_request` for a plugin from another NAS |

Errors: `400 invalid_request` (keyword or category), `409 search_no_sources` (no source enabled and set up for the search), `429 rate_limited`, `404 not_found`, `502 search_torrent_failed` (the source gave no valid `.torrent`), `400 search_plugin_invalid` (the message says what is wrong), `409 search_plugin_exists` (the id is taken, by a built-in source too), `409 search_plugin_changed` (the plugin's address now serves another file than the one inspected; installing from an address without `sha256` is `400 invalid_request`), `409 search_plugin_builtin` (built-in sources cannot be deleted), `409 search_plugin_limit` (50 sources at most), `502 fetch_failed` (the plugin's address could not be read); for plugins from another NAS also `400 search_consent_required` (no `accept_risk: true`, or `sha256` is not the file's) and `409 search_runtime_unavailable` (this NAS cannot run such plugins; the message is the reason), administrators in the web UI only like every `/search/plugins` endpoint. The plugin format is described in [SEARCH-PLUGINS.md](SEARCH-PLUGINS.md).

### 2.2 Download Station clients

Qget, Qfile and browser extensions keep using the Download Station API at `/downloadstation/V4/` (always also at `/DownloadCenter/downloadstation/V4/`) with a QTS session. Where it behaves differently from the official package:

- Until the session is known, only the first 64 KiB of a request body are read: a large form post must carry `sid` in the query, in the `QDS_SID` cookie or within those first 64 KiB. Torrent uploads (`Task/AddTorrent`), whose official clients send `sid` after the file, are read whole, two at a time at most.
- `Misc/Logout` ends a session taken from the cookie (no `sid` parameter) only for a POST from this NAS's own pages.
- `Misc/Login` stops trying a client's sign-ins after 10 failures within 10 minutes, without asking QTS.
- The `caller` a client gives (the program shown in a task's details) is cut to 32 printable characters and cannot be one of the names Download Center gives its own callers; such a value becomes "Download Station".

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
| `task.likely_dead` | A torrent has probably stopped being shared (its `source_health` became `likely_dead`): `waiting_secs`, `ever_connected`, `reason` (`unregistered`, `no_seeders`, `incomplete`). Once per period without sources, not again after a restart; again only after the task had sources. Nothing is done to the task |
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

Created per owner (`notify:manage`). Accounts that are not administrators have up to 10 channels, webhooks included (`409 too_many_channels`). Each has a URL, the event types it wants, and a secret. Left empty, the secret is generated and returned once when the webhook is created (the web UI shows it then).

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
- Outbound HTTP uses Go clients from `internal/netutil` with explicit timeouts; for non-admin owners the target is resolved and forbidden ranges are refused after resolution, at dial time or (through a proxy) before the request (DNS rebinding, redirects). Such a checked request through an HTTP proxy goes through a CONNECT tunnel to the address that was checked; a proxy that refuses the tunnel makes the request fail rather than resolve the name itself. This applies to regular users' webhooks, link extraction and `.torrent` links fetched over plain http through such a proxy, and to search sources.
