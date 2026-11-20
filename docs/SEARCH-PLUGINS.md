# Search plugins

Download Center searches for torrents by keyword (Search in the web UI, `POST /search` in the REST API, see [INTEGRATION.md](INTEGRATION.md) §2.1). Each place a search asks is a **search source**. This document describes the source format exactly as Download Center reads it, what it does with the answers, and the rules a source must follow to be installed.

Search sources are third-party sites. Download only what you have the right to download; administrators are responsible for the sources they add.

## 1. What a search source is

A search source is a plugin: a JSON file that describes one GET request to a fixed host and where each result's fields are in the answer. Download Center interprets the description. A plugin file cannot run code, use another method or a request body, reach a host other than the one written in it, or read another source's settings.

Search plugins written for the download managers of other NAS makers are a different kind: they are programs. Download Center can run Synology search modules and the search part of QNAP add-ons, in a restricted environment and only after an administrator agrees to the terms (section 9).

| `type` | Answer | What the plugin gives |
|---|---|---|
| `json` | JSON | The request address, the path of the result list and a path for each field |
| `rss` | RSS 2.0, RSS 1.0 or Atom (OPDS included) | The request address and the item value for each field |
| `torznab` | Torznab | Nothing but a name: the administrator gives the server address and API key, and the answer is read with the Torznab rules (section 3). For Jackett, Prowlarr and other Torznab servers |

The built-in sources (Internet Archive, Kiwix, Fedora, Arch Linux, FOSS Torrents) are plugins in this format, validated like any other. They cannot be deleted or replaced, only switched on or off. There are at most 50 sources, built-in ones included.

Nothing is asked of a source unless someone searches, tests the source, or installs a plugin from its address. A search asks every source that is switched on, has its required settings, has the category searched and may be used by the account searching (section 2), at most 6 at a time, once each (no retries).

## 2. Adding and managing sources

Administrators manage sources in **Settings › Search**, in the web UI only (access tokens and regular users cannot).

- **Add source** › **Plugin file** takes a file, pasted JSON or an `https://` address to download the plugin from. Download Center validates it (section 7) and shows a review before anything is installed: name, version, description, the host it connects to, what it sends, the headers it adds and the settings to fill in. **Add** installs it (**Replace** when a custom source with that `id` is installed). Errors are shown in the same window.
- The API installs a plugin **switched off**, whatever its type; the review's "Use it for searches right away" (on by default) switches it on after adding. Otherwise fill in its settings, test it, then switch it on. A Torznab server added with its form in Settings is switched on at once; one installed from a `torznab` plugin file (4.8) is switched off like any plugin file. A source whose required setting is empty is not searched (`needs_setup` in the API).
- **Who can use it**: every source is for **everyone** or for **administrators only**. Built-in sources and plugins without a secret setting start for everyone; Torznab servers, plugins with a secret setting and plugins from another NAS start for administrators only, because their results can carry what identifies the account on the site (a private tracker's passkey in a tracker address, for example). A source for administrators only is not listed for regular users, not searched for them and does not count when the page decides whether they see Search; an access token counts as its owner.
- A plugin installed from an address must be the file that was checked: if the address serves another file when **Add** is clicked, nothing is installed and the new file is checked and shown again.
- **Test** asks the source once with its `test_query` (`ubuntu` when it has none) in every category and reports the number of results and the time taken, or why it failed. A test needs the required settings.
- **Export** saves a source's definition as `<id>.json`, without any setting values.
- When a custom source with the same `id` is installed, a new file can **replace** it. A new definition keeps a source's on/off state, audience and saved values only with the same type, the same full request address (scheme, host, port, path and parameters), the same link fields (how the magnet link, `.torrent` and page addresses of results are built) and the same settings with the same `secret` flags; otherwise it starts switched off with the default audience and without saved values (the review says which of the two happens). No version comparison is made. A built-in source cannot be replaced, and a plugin file and a plugin from another NAS cannot replace each other (`search_plugin_exists`, already when the file is checked). Testing a definition whose id is installed uses the installed values only under the same condition.
- Custom definitions are stored as validated and written out again in `data/search/plugins/<id>.json`; the on/off switches and setting values (API keys included) in `data/search/state.json`. Both are readable by the daemon only.

The REST endpoints behind these (administrators signed in to the web UI only; details and error codes in [INTEGRATION.md](INTEGRATION.md) §2.1):

| Method & path | Does |
|---|---|
| `GET /search/plugins` | Lists every source with its state, host, header names and settings (never a secret's value) |
| `POST /search/plugins/inspect` | Validates `{"json": "…"}` or `{"url": "https://…"}`, or checks a plugin from another NAS `{"file": "<Base64>", "filename": "x.dlm"}` (section 9), without saving or running anything |
| `POST /search/plugins` | Installs a plugin (`"replace": true` to replace, `"settings": {…}` to fill them in), adds a Torznab server `{"torznab": {"name", "url", "apikey"}}`, or installs a plugin from another NAS `{"file", "filename", "accept_risk": true, "sha256"}` (section 9) |
| `POST /search/plugins/test` | Asks a plugin or Torznab server once without saving it |
| `PUT /search/plugins/{id}` | Switches a source on or off, changes setting values, renames a Torznab source, sets `"audience": "all"` or `"admins"` |
| `POST /search/plugins/{id}/verify` | Asks a plugin from another NAS whether its account works: `{"ok": true}` or `{"ok": false, "error": "…"}` |
| `DELETE /search/plugins/{id}` | Removes a custom or Torznab source |
| `POST /search/plugins/{id}/test` | Tests an installed source |
| `GET /search/plugins/{id}` | Exports the definition |

## 3. Torznab servers (Jackett, Prowlarr)

Torznab is the search API of indexer managers such as Jackett and Prowlarr. You install and run such a server yourself and choose the sites it searches; Download Center only talks to the server.

**The server cannot run on this NAS.** Requests to search sources never go to a loopback address (`localhost`, `127.0.0.1`, `::1`) or to any address of the NAS itself, and that includes a port the NAS forwards to a container. Run the server on another machine, or give its container its own address on the LAN. Other machines' LAN addresses are allowed.

To add one, open **Settings › Search › Add source**, choose Torznab server and give:

- a **name**, at most 60 characters. The source's id is made from it: its letters a to z (lower-cased) and digits, with one hyphen for each run of other characters between them, cut at about 32 characters; `-2`, `-3` … are added when the id is taken, and `torznab` is used when fewer than two characters are left. A Torznab source can be renamed later; the id stays.
- the **server address**: `http` or `https`, with no user name, password or fragment, and without the API key (an `apikey` parameter is refused: the address is not stored as a secret). Other query parameters in it are kept. Examples:
  - Jackett, every configured indexer at once: `http://192.168.1.20:9117/api/v2.0/indexers/all/results/torznab`, or the Torznab feed address Jackett shows for one indexer.
  - Prowlarr, one source per indexer: `http://192.168.1.21:9696/<indexer id>/api`, for example `http://192.168.1.21:9696/3/api`.
- the **API key** of the server (shown in the server's own web interface). It is a secret: never shown again, exported or logged.

The request is `<server address>?t=search&q=<keyword>&limit=100&apikey=<key>`, plus `cat` for a category. Parameters of the address with these names are replaced, and a `cat` in the address is dropped. The keyword's white space is collapsed; nothing else is changed.

| Category searched | `cat` |
|---|---|
| all | (none) |
| video | `2000,5000` |
| audio | `3000` |
| software | `4000` |
| books | `7000` |
| other | `8000` |

Each `<item>` of the answer is read like this (`torznab:attr` and `newznab:attr` alike):

| Result | Read from |
|---|---|
| title | `<title>` |
| infohash | the `infohash` attr |
| magnet link | the `magneturl` attr, or a magnet link given as the enclosure or `<link>` |
| `.torrent` | `<enclosure url>`, else `<link>` |
| size | `<size>`, else `<enclosure length>`, else the `size` attr |
| seeders | the `seeders` attr |
| leechers | the `leechers` attr, else `peers` minus `seeders` (at least 0) |
| date | as `pubDate` (section 4.7) |
| page | `<comments>`; never `<guid>`, which can be a tracker's download address with a passkey in it |
| category | the first standard Newznab number among the `category` attrs and `<category>` elements: 1000s and 4000s `software`, 2000s and 5000s `video`, 3000s `audio`, 7000s `books`, 6000s and 8000s `other`; other numbers (below 1000, 9000 and up, which includes site-specific ones) are skipped; `other` when none |

An answer whose root is `<error code="…" description="…"/>` is the source's failure, whatever the HTTP status: codes 100 to 199 are reported as "The server refused the API key", 500 and 501 as "The source is busy, try again later", any other code with the server's own description (control characters, addresses, the keyword and the key removed, at most 120 characters).

The server's `.torrent` links carry its API key. They are fetched through the NAS (by the result's `ref`) and never shown to the caller; neither is a page on the server's host. A download link that redirects to a magnet link (Prowlarr does this) gives that magnet link, rebuilt by Download Center.

## 4. The plugin file

### 4.1 Example

A fictional `json` source using most of the format:

```json
{
  "schema": 1,
  "id": "example-library",
  "name": "Example Library",
  "version": "1.2",
  "description": "Openly licensed films, music and books from the Example Library.",
  "homepage": "https://www.example.org/",
  "type": "json",
  "prefer": "torrent",
  "webseed": true,
  "request": {
    "url": "https://api.example.org/v2/search?q={query}&kind={category}&rows={limit}&page={page}&lang={setting:language}",
    "headers": {
      "Accept": "application/json",
      "Authorization": "Bearer {setting:apikey}"
    },
    "strip": "\"*:()"
  },
  "settings": [
    {"id": "apikey", "label": "API key", "secret": true, "required": true},
    {"id": "language", "label": "Language (optional, for example en)"}
  ],
  "categories": {
    "video": "film",
    "audio": "music",
    "books": "text",
    "other": ""
  },
  "test_query": "big buck bunny",
  "response": {
    "items": "data.results",
    "require": ["available"],
    "fields": {
      "title": "title",
      "size": "files.total_bytes",
      "infohash": "torrent.btih",
      "magnet": "torrent.magnet",
      "torrent": "https://dl.example.org/torrents/{id}.torrent",
      "page": {"from": "links.details", "replace": ["http://", "https://"]},
      "date": "published",
      "seeders": "stats.seeders",
      "leechers": "stats.leechers",
      "category": "kind"
    }
  }
}
```

With the language setting `en`, a search for `big "buck" (bunny)` in video sends

```
GET https://api.example.org/v2/search?q=big+buck+bunny&kind=film&rows=100&page=1&lang=en
Accept: application/json
Authorization: Bearer <the API key>
```

The source is not asked in a software search (it does not declare `software`). Suppose it answers:

```json
{
  "data": {
    "total": 3,
    "results": [
      {
        "id": "big-buck-bunny-1080p",
        "title": "Big Buck Bunny (1080p)",
        "available": true,
        "kind": "film",
        "published": "2008-05-20T00:00:00Z",
        "files": {"count": 1, "total_bytes": 276445467},
        "torrent": {"btih": "3F9A2C0B7D5E41A8B6C2E9F0D1A47B3C5E8F2A61"},
        "links": {"details": "http://www.example.org/films/big-buck-bunny-1080p"},
        "stats": {"seeders": 31, "leechers": "2"}
      },
      {
        "title": "Field Recordings, Vol. 2",
        "available": true,
        "kind": "music",
        "published": 1717401600,
        "torrent": {
          "magnet": "magnet:?xt=urn:btih:9b2e4d6f8a0c1e3b5d7f9a1c3e5b7d9f0a2c4e6b&dn=x&tr=udp%3A%2F%2Ftracker.example.org%3A6969%2Fannounce&xs=http%3A%2F%2Fother.example.net%2Fx.torrent"
        },
        "stats": {"seeders": 0}
      },
      {
        "id": "withdrawn-title",
        "title": "Withdrawn Title",
        "available": false,
        "torrent": {"btih": "0123456789abcdef0123456789abcdef01234567"}
      }
    ]
  }
}
```

That gives two results:

1. "Big Buck Bunny (1080p)": infohash `3f9a2c0b7d5e41a8b6c2e9f0d1a47b3c5e8f2a61`, `.torrent` `https://dl.example.org/torrents/big-buck-bunny-1080p.torrent` and no magnet link (`prefer: "torrent"`), page `https://www.example.org/films/big-buck-bunny-1080p`, size 276445467, 31 seeders, 2 leechers, category `video`.
2. "Field Recordings, Vol. 2": no `id`, so no `.torrent` address; the infohash comes from the magnet link, which is rebuilt with its one tracker (`xs` and `dn` dropped) as `magnet:?xt=urn:btih:9b2e4d6f8a0c1e3b5d7f9a1c3e5b7d9f0a2c4e6b&dn=Field+Recordings%2C+Vol.+2&tr=udp%3A%2F%2Ftracker.example.org%3A6969%2Fannounce`; 0 seeders, leechers unknown, category `audio`.

Both are marked `webseed`. "Withdrawn Title" is skipped by `require`.

### 4.2 Top-level keys

| Key | Required | Rules |
|---|---|---|
| `schema` | yes | `1` |
| `id` | yes | 2 to 40 lower-case letters, digits or hyphens, starting with a letter or digit. Not a built-in source's id (`archlinux`, `fedora`, `fosstorrents`, `internet-archive`, `kiwix`). Keep it across versions: a replacement is matched by it |
| `name` | yes | At most 60 characters |
| `version` | no | At most 20 characters; shown only |
| `description` | no | At most 500 characters |
| `homepage` | no | An absolute `http` or `https` address with a host, at most 2048 bytes |
| `type` | yes | `json`, `rss` or `torznab` |
| `match` | no | `"local"`: the source has no keyword search; Download Center fetches its whole catalogue and matches titles itself (section 5.4). Not for `torznab` |
| `prefer` | no | `"magnet"` (default) or `"torrent"`: a result that has both an infohash and a `.torrent` address is offered as the `.torrent` only. For sources whose torrents rely on web seeds: a magnet link of such a torrent would never get its file list |
| `webseed` | no | `true`: the source's torrents carry web seeds and download even without seeders; its results are marked `webseed` |
| `default_enabled` | no | Whether a built-in source starts switched on; ignored for sources an administrator adds |
| `test_query` | no | The keyword Test uses, at most 200 characters (`ubuntu` when absent). For `match: "local"`, use words some titles contain |
| `settings` | no | Values the administrator fills in (4.3). Not for `torznab` |
| `categories` | no | The categories the source has results in (4.4). Not for `torznab` |
| `request` | `json`, `rss` | The request (4.5). Not for `torznab` |
| `response` | `json`, `rss` | Where the results are (4.6). Not for `torznab` |

Any other key is refused, in the plugin and in `request`, `settings`, `response`, `fields` and field objects alike. Text values (`name`, `version`, `description`, `test_query`, setting labels, category values, `strip`) must be valid UTF-8 without control characters (U+0000 to U+001F, U+007F to U+009F); their lengths are in characters, other lengths are in bytes as noted.

### 4.3 Settings

Up to 10 entries `{"id", "label", "secret", "required"}`:

- `id`: a lower-case letter followed by up to 31 lower-case letters, digits or underscores; unique within the plugin.
- `label`: shown next to the field as written, at most 60 characters, required.
- `secret`: the value is never returned by the API, exported or logged, and is removed from a Torznab error text. A result's `.torrent` address that contains it (as written, URL-escaped, ignoring case) is fetched through the NAS and not shown; a page address that contains it is dropped. Leaving a secret empty when editing keeps the saved value.
- `required`: the source is not searched (and cannot be tested) until it has a value.

Values are at most 1024 bytes, without control characters, and are trimmed when saved; a non-secret left empty is cleared. A setting reaches the request only through `{setting:<id>}` in `request.url` or a header value (4.5).

### 4.4 Categories

Download Center's categories are `video`, `audio`, `software`, `books` and `other`; a search asks one of them or `all`. `categories` maps each category the source has results in to the source's own value for it (at most 200 characters, may be empty), for example `{"video": "movies", "audio": "audio", "books": ""}`. `all` is not a key.

- A search in a category asks only the sources that declare it; `all` asks every source. A source without `categories` is asked in every category.
- The value goes into `{category}` (4.5) when that category is searched; `{category}` is empty for `all` and for an empty value.
- A result's `category` field is matched against the values (5.3).

### 4.5 Request

| Key | Rules |
|---|---|
| `url` | Required. The address template, at most 2048 bytes, without spaces, `#` or `\` |
| `headers` | Up to 10 headers, sent as written; values may use `{setting:<id>}` |
| `strip` | Characters taken out of the keyword before it goes into the address (each becomes a space), at most 64 characters. For query languages that treat them as operators |

A template is text with variables in braces. A variable's name is everything between `{` and `}`; braces cannot be written literally and do not nest. The variables of `url`:

| Variable | Value |
|---|---|
| `{query}` | The keyword: trimmed, `strip` characters replaced by spaces, runs of white space made one space |
| `{limit}` | `100` |
| `{page}` | `1` |
| `{category}` | The `categories` value of the category searched; empty for `all` or an empty value |
| `{setting:<id>}` | The value of a setting the plugin declares; empty when not set |

Every variable is URL-query-escaped wherever it stands (a space becomes `+`, `/` `%2F`, `&` `%26`, `#` `%23`), so no value can add parameters or change the host. Put variables in the query string: in the path, a space arrives as `+`.

Rules for `url`:

- It begins with `https://`.
- The host and port are written out: no variable, no user name, no IP address in any form (`192.0.2.10`, `[::1]`, `2130706433`, `0x7f.1`), not `localhost` or a name ending in `.localhost`. A port, when given, is 1 to 65535. After filling in sample values the address must still parse to the same `https` host.
- `{query}` appears in it, except with `match: "local"`, where it must not.
- Only the variables above; `{setting:<id>}` only for declared settings.
- When nothing is left of the keyword after `strip`, the source is not asked and gives no results.

The source may redirect the request (section 8); the answer is read from wherever it ends.

Rules for `headers`:

- A name is an HTTP token of 1 to 64 characters. Not allowed, in any case: `Host`, `Cookie`, `Cookie2`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Keep-Alive`, `Upgrade`, `TE`, `Trailer`, `Expect`, `Forwarded`, `User-Agent`, `Accept-Encoding`, and every `Proxy-*` and `X-Forwarded-*`.
- A value is at most 1024 bytes without control characters. Its only variable is `{setting:<id>}`, put in as it is (`{query}` and the others are refused).
- `Accept` may be set. Otherwise Download Center sends `application/json` for `json` and `application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.8` for `rss` and `torznab`.
- The headers go to the plugin's host only: they are dropped when a redirect changes the host, the port or the scheme, and they are not sent when a result's `.torrent` is fetched, so a `.torrent` address must work without them.

### 4.6 Response

| Key | Rules |
|---|---|
| `items` | `json` only: the path of the result list; empty or absent when the answer is the list. Not allowed for `rss` |
| `require` | Up to 10 paths (`json`) or value names (`rss`) an item must have: an item where one of them is missing, `null`, `false`, empty or 0 (also as text: `"false"`, `"0"`) is skipped |
| `fields` | Required. Where each part of a result comes from |

| Field | Kind | Meaning |
|---|---|---|
| `title` | text | Required |
| `size` | text | Size in bytes |
| `infohash` | text | 40 hex digits or 32 Base32 characters |
| `magnet` | address | A magnet link; only its infohash and trackers are used |
| `torrent` | address | The `.torrent` address |
| `page` | address | The details page |
| `date` | text | Publication date |
| `seeders` | text | Seeder count |
| `leechers` | text | Leecher count |
| `category` | text | The source's category value, mapped through `categories` |

`title` and at least one of `infohash`, `magnet` and `torrent` are required.

A field is a string, or an object `{"from": "…", "replace": ["old", "new"]}`. The string (or `from`, 1 to 512 bytes) is a source (4.7), or a template when it contains a brace. `replace` replaces the first occurrence of `old` in the value with `new`, once, as plain text (no patterns); it holds exactly two strings, the first not empty, each at most 200 bytes.

A template has 1 to 10 variables, each a source: `"https://dl.example.org/{id}.torrent"`, `"{title} {ns:flavour}"`. Values are trimmed. In address fields (`magnet`, `torrent`, `page`) each value is URL-path-escaped (`/` becomes `%2F`, a space `%20`) and the field is empty when any value is empty, so no address is built around a missing identifier. In text fields values are put in as they are, and the field is empty only when all its values are empty. `replace` applies after the template is filled in.

### 4.7 Field sources

**`json`**: a path relative to the item. Names separated by dots; a number indexes a list (`files.0.name`); up to 10 parts, each 1 to 100 characters without `.`, braces, white space or control characters. A string is taken as it is, a number as written, `true`/`false` as text, a list gives its first string, number or boolean; objects and `null` are empty. Template variables are paths too (`{torrent.btih}`).

**`rss`**: one of these names (only an item's direct child elements are read; `title`, `link`, `guid` and `enclosure` are the elements of RSS 2.0 without a namespace, RSS 1.0 or Atom):

| Name | Value |
|---|---|
| `title` | The text of `<title>` |
| `link` | The text of `<link>`; else the `href` of an Atom `<link>` without `rel` or with `rel="alternate"`, a `text/html` one first |
| `guid` | `<guid>`, or Atom `<id>` |
| `pubDate` | The first of `<pubDate>`, `<published>`, `<issued>`, `<updated>`, `<date>`, in any namespace (`dc:date`) |
| `enclosure.url` | The `url` of `<enclosure>`; else the `href` of an Atom `<link>` whose `rel` contains `acquisition` or `enclosure` (OPDS) |
| `enclosure.length` | The `length` of the same element |
| `attr:<name>` | The `value` of the first `torznab:attr` or `newznab:attr` whose `name` is `<name>` |
| `ns:<name>` | The text of the first child element with that local name, in any namespace (`ns:infoHash`, `ns:seeders`, `ns:size`) |

`<name>` is a letter or `_` followed by up to 63 letters, digits, `_`, `.` or `-`. `description` is not available.

An `rss` example, a catalogue feed matched on the NAS:

```json
{
  "schema": 1,
  "id": "example-os-releases",
  "name": "Example OS releases",
  "version": "1.0",
  "type": "rss",
  "match": "local",
  "request": {"url": "https://releases.example.net/torrents.rss"},
  "categories": {"software": ""},
  "test_query": "example os",
  "response": {
    "fields": {
      "title": "title",
      "torrent": "enclosure.url",
      "size": "enclosure.length",
      "infohash": "ns:infoHash",
      "page": "link",
      "date": "pubDate"
    }
  }
}
```

### 4.8 Torznab plugin files

A `torznab` plugin has `schema`, `id`, `name` and `type`, and may have `version`, `description`, `homepage`, `prefer`, `webseed` and `test_query`; `request`, `response`, `settings`, `categories` and `match` are refused. Its settings are always the server address (required) and the API key (secret), and it has every category. This is what exporting a Torznab source gives:

```json
{
  "schema": 1,
  "id": "jackett-lan",
  "name": "Jackett (LAN)",
  "type": "torznab"
}
```

Installed from a file, a Torznab source is switched on but is not searched until its server address is filled in.

## 5. From answer to results

### 5.1 Reading the answer

- **`json`**: decoded as JSON (numbers kept as written). `items` must lead to a list, otherwise the answer cannot be read. At most 5000 items are read.
- **`rss`, `torznab`**: XML whose root is `rss`, `RDF` or `feed`; a root `<error code="…" description="…"/>` is a Torznab error (section 3), for `rss` sources too; anything else cannot be read. Every `<item>` or `<entry>` at any depth, not inside another, is an item: at most 5000 items, 200 child elements per item, 64 KiB of text per element, 64 levels of nesting. Encodings: UTF-8, US-ASCII, ISO-8859-1 (Latin-1) and Windows-1252 (read as Latin-1). XML's and HTML's named entities are expanded; any other entity, including one a DOCTYPE declares, makes the answer unreadable.
- Relative addresses in the answer are resolved against the address it came from (after redirects).

### 5.2 Normalisation

Each item becomes a result:

- **title**: invalid UTF-8, control characters and bidirectional formatting characters (U+200E, U+200F, U+202A to U+202E, U+2066 to U+2069, U+FEFF) removed; runs of white space made one space; cut to 300 characters. It is shown as text, never as HTML. An item without a title is dropped.
- **infohash**: 40 hex digits in either case, or 32 Base32 characters; kept as lower-case hex. Anything else is ignored.
- **magnet**: only its `btih` infohash (hex or Base32) and its trackers are read: `http`, `https` and `udp` (with a port), at most 20, duplicates dropped, and never `localhost` or a literal local or NAS address. Everything else in it (`dn`, `xs`, `as`, `ws` …) is dropped; links over 16 KiB are ignored. Its infohash is used when `infohash` gave none.
- **torrent, page**: `http` or `https` with a host, no user name or password, at most 2048 bytes, no spaces or backslashes; anything else is dropped. A magnet link in `torrent` counts as the magnet link (when `magnet` gave none).
- An item with neither an infohash nor a `.torrent` address is dropped.
- **magnet link of the result**: built by Download Center as `magnet:?xt=urn:btih:<infohash>&dn=<title>&tr=…` whenever there is an infohash, except that with `prefer: "torrent"` a result with a `.torrent` address gets none. A source's magnet link is never passed on.
- **size** (more than 0), **seeders**, **leechers** (0 or more): JSON numbers or numeric text, whole or decimal (`1.5e3`); negative or other values are unknown.
- **date**: Unix seconds (milliseconds when the number is larger than 10^11), RFC 3339, `2006-01-02T15:04:05`, RFC 1123 and its RSS forms (`Mon, 2 Jan 2006 15:04:05 -0700`, with a zone name, without a zone, without seconds, without the weekday), `2006-01-02 15:04:05` and `2006-01-02`. Times without a zone are UTC. Anything else is unknown.
- Unknown values are left out of the result.

### 5.3 Categories of results

- With a `category` field, its value (trimmed, ignoring case) is looked up among the `categories` values; the first of `video`, `audio`, `software`, `books`, `other` with that value is the result's category, `other` when none has it. Empty values never match.
- Without a `category` field, a result gets the category searched when the source declares it, else the source's only category when it declares exactly one, else `other`. A source without `categories` and without a `category` field therefore gives `other` results in every search.

### 5.4 Sources matched on the NAS

With `match: "local"` the request address has no `{query}`: Download Center fetches the whole catalogue (within the 2 MiB and 5000 item limits) and keeps the results whose title contains every word of the keyword, ignoring case (parts of words count). The catalogue is kept in memory for an hour per source, then asked again with `If-None-Match` and `If-Modified-Since`; a `304 Not Modified` keeps the copy. It is dropped when the source's definition or settings change. Test always fetches it anew and does not keep it.

A source gives at most 100 results per search, in its own order (after matching).

### 5.5 Merging

Results with the same infohash from several sources become one result that lists every source, with the highest seeder and leecher counts and the trackers of all (at most 20). The first source's title, size, date, page and `.torrent` address are kept and gaps are filled from the others; a category `other` gives way to another source's. The merged result has a magnet link when any of its sources gives one. Results without an infohash are never merged.

### 5.6 Failures

A source that fails is reported with one of these reasons, never with the address, the keyword or a setting:

| Reason | When |
|---|---|
| Timed out | No complete answer within 15 s |
| Could not connect | Name not resolved, connection or TLS failure, more than 3 redirects, a redirect to something other than `http` or `https` |
| This address is not allowed: a search source cannot be on this NAS or a local address | The address guard (section 8) |
| A proxy is required for this address, and none can be used | Regular users must use a proxy and none applies |
| The answer is too large | More than 2 MiB |
| Could not read the answer | Not JSON or XML as the plugin says; a search answer that redirects to a magnet link |
| The source answered with HTTP {n} | Any other status that is not 2xx |
| The server refused the API key | 401 or 403 while the source has a secret value set; Torznab error 100 to 199 |
| The source is busy, try again later | 429 or 503; Torznab error 500 or 501 |
| The source reported an error: {text} | Any other Torznab error |
| Fill in the settings of this source first | A test without the required settings |
| The plugin failed | A plugin from another NAS (section 9) stopped with an error, or its answer was too large or unreadable |
| The plugin did not finish in time | A plugin from another NAS ran for 30 s |
| This NAS cannot run this plugin: {reason} | The NAS cannot run plugins from another NAS now (section 9.2) |

An `rss` or `torznab` answer that is not 2xx (other than 429) is first read for a Torznab error; a `json` answer that is not 2xx is never read.

## 6. Limits

| What | Limit |
|---|---|
| Plugin file (also when downloaded) | 64 KiB |
| Search answer | 2 MiB |
| `.torrent` fetched for a result | 16 MiB |
| Time per request, redirects and answer included | 15 s |
| Redirects | 3 |
| Sources | 50, built-in ones included |
| Items read from one answer | 5000 |
| Results per source per search | 100 |
| Result title | 300 characters |
| Trackers in a magnet link | 20 |
| Settings / headers / `require` entries | 10 each |
| Setting and header values | 1024 bytes |
| Request, homepage, result and Torznab addresses | 2048 bytes |
| `id` / setting id / header name | 40 / 32 / 64 characters |
| `name` / `version` / `description` / `test_query` | 60 / 20 / 500 / 200 characters |
| Setting label / category value / `strip` | 60 / 200 / 64 characters |
| Field `from` / each `replace` string | 512 / 200 bytes |
| Variables per field template | 10 |
| Parts of a path / characters per part | 10 / 100 |
| `attr:` and `ns:` names | 64 characters |
| XML child elements per item / text per element / nesting | 200 / 64 KiB / 64 |
| Torznab error text shown | 120 characters |
| Keyword | 2 to 200 characters |
| Searches per account | 20 per minute; at most 3 kept, for 30 minutes after they finish |
| Sources asked at the same time by one search | 6 |
| Catalogue of a `match: "local"` source | kept 1 hour |

## 7. Validation

A plugin is checked when it is installed, inspected or tested, and again whenever Download Center starts (a stored plugin that no longer passes is not loaded). Only the first problem is reported, as `400 search_plugin_invalid` with one of these messages (`{…}` is filled in):

| Message | Cause |
|---|---|
| The plugin is larger than 64 KiB | |
| The plugin is not valid JSON: {detail} | Syntax, a value of the wrong JSON type, data after the object |
| Unknown field "{name}" | A key the format does not have |
| The field schema has an unsupported value: {n} | `schema` is not 1 |
| The id must be 2 to 40 lower-case letters, digits or hyphens, starting with a letter or digit | |
| The field {field} is missing | `name`, `request.url`, a setting's `label` |
| The field {field} is too long | A text over its length; more than 10 settings, headers or `require` entries |
| The field {field} is not valid | Control characters; `homepage` not an http(s) address; a bad or repeated setting id; a bad header name or value; a field's `from` empty or too long, a bad template; a bad `replace` |
| The field {field} has an unsupported value: {value} | `type`, `match`, `prefer`, a `categories` key |
| A source of type {type} cannot have the field {field} | `torznab` with `request`, `response`, `settings`, `categories` or `match`; `rss` with `response.items` |
| The request address is not valid | Too long, spaces, `#` or `\`, unbalanced braces, a user name, a bad port |
| The request address must use https | |
| The host name of the request address must be written out, not taken from a variable | A variable in the host or port, or sample values change the host |
| The request address must name a host, not an IP address or localhost | |
| The request address must contain the search keyword | No `{query}` without `match: "local"` |
| The request address of a source matched on the NAS cannot contain the search keyword | `{query}` with `match: "local"` |
| Unknown variable {name} | A variable that does not exist, an undeclared setting, `{query}` in a header, a bad path in a field template |
| The header {name} is not allowed | |
| The field mapping must give a title | No `response` or no `title` |
| The field mapping must give an infohash, a magnet link or a .torrent address | |
| The path {path} is not valid | A bad path or `rss` value name in `items`, `require` or a field |

Other refusals:

| Status | Message | Cause |
|---|---|---|
| `409 search_plugin_exists` | A search source with this id already exists | A built-in source's id; an installed custom source's id without replace; replacing between `torznab` and another type |
| `409 search_plugin_limit` | There can be at most 50 search sources | |
| `400 search_plugin_invalid` | Give the plugin as JSON or as an https address | Neither was given |
| `400 search_plugin_invalid` | The plugin address must be an https address | Installing from an address that is not `https` |
| `502 fetch_failed` | Could not get the plugin: {reason} | The address could not be read (reasons as in 5.6) |
| `400 search_plugin_invalid` | Unknown setting {id} | A value for a setting the plugin does not declare |
| `400 search_plugin_invalid` | Enter a name for the Torznab server | |
| `400 search_plugin_invalid` | Enter the address of the Torznab server | |
| `400 search_plugin_invalid` | The server address must be an http or https address | Torznab: another scheme, no host, a user name or a fragment |
| `400 search_plugin_invalid` | Enter the API key in its own field, not in the server address | Torznab address with `apikey` |

## 8. Security boundary

- Only administrators, in the web UI, add, change or remove sources. Searching needs the `search:use` permission; a search belongs to the account that started it.
- A plugin file is data: one GET to the host written in it, over `https`, with every variable escaped (a redirect may lead elsewhere, under the rules below). A Torznab address may be `http` or `https` and may be a LAN address. Plugins from another NAS are programs with stricter rules of their own (section 9).
- Every request for a plugin file or a Torznab source (searching, testing, downloading a plugin file, fetching a result's `.torrent`, whatever source the result came from) goes through one guarded client:
  - It never connects to a loopback, link-local (`169.254.0.0/16`, `fe80::/10`), unspecified (`0.0.0.0`, `::`) or multicast address, or to any address of the NAS's own interfaces. Literal addresses (IPv4 in any form) and `localhost` are refused before anything is sent, names after they are resolved (only allowed addresses are used), and the same on every redirect. Other machines' private LAN addresses are allowed.
  - It uses the proxy that the proxy settings choose for that address for a regular user's download (site rules, the default proxy for URL downloads, its no-proxy list), whoever searches. Through a proxy, the target is checked before the request is handed over: literal addresses and `localhost` are refused, and a name is resolved and refused when none of its addresses is allowed, except with a SOCKS5 proxy that resolves names itself (remote DNS), where only literal addresses are checked. Through an HTTP proxy the request goes through a CONNECT tunnel to the address that was checked; a proxy that refuses the tunnel makes the request fail, it is never left to resolve the name itself. When regular users must use a proxy and none applies, or the proxy cannot be reached, the request fails; it never falls back to a direct connection.
  - At most 3 redirects, to `http` or `https` only; a redirect to a magnet link ends the request. No `Referer` is sent, and the plugin's headers are dropped when a redirect changes the host, the port or the scheme.
  - It sends `User-Agent: DownloadCenter/<version> (+https://github.com/tasict/DownloadCenter)`, `Accept` and the plugin's headers, and nothing of the caller: no cookie, session, token, user name or address. Cookies the source sets are not kept. The connection is closed after each request.
  - 15 s, and the size limits of section 6.
  - A result's `.torrent` is fetched by the NAS only from public addresses on ports 80 and 443 (never a private LAN, carrier-grade NAT or reserved address), except from the host of the administrator's Torznab server, which keeps the usual rule on any port. Every failure reads "This source did not give a valid .torrent file", and an account fetches at most two at a time.
- Results are checked as in section 5.2: text titles, `http`/`https` addresses only, magnet links rebuilt from the infohash, keeping only trackers that are not `localhost` or a literal local or NAS address. A result's `.torrent` or tracker address that carries one of the source's secret values is dropped unless it is on the source's own host (the host of the plugin file's request, or the administrator's Torznab server); a page address carrying one is always dropped. A `.torrent` is fetched through the NAS and must be a valid torrent. Searching never adds a download: adding goes through the normal add rules of the account.
- Secret settings never leave the NAS except in the requests the plugin describes (section 4.3). Keywords, addresses and secrets are not logged; failures are logged with the source id and a fixed reason only.

## 9. Search plugins from another NAS

Download Center can use search plugins written for the download managers of Synology and QNAP NAS. Download Center is not affiliated with Synology or QNAP; it implements the plugins' interfaces as their makers document them, with code of its own, and is compatible with plugins that keep to those interfaces. Such a plugin is a program: it runs on this NAS, in a restricted environment, and only after an administrator agrees to the terms below.

### 9.1 What is supported

| File | Supported | Not supported |
|---|---|---|
| Synology Download Station search module (`.dlm`) | Modules whose `INFO` has `"type": "search"`: `prepare`, `parse` with `addResult`, `addJsonResults` and `addRSSResults`, and `VerifyAccount` for modules with `accountsupport` | File-hosting modules (`.host`, or any other `type`) are refused |
| QNAP Download Station add-on (`.addon`) | Add-ons that declare `ISearch`: `Search` returning `SearchLink` objects, `Verify` when they declare `IVerify` and have an account, `Ajax` for their requests | The download, RSS and post-processing interfaces (`IDownload`, `IRss`, `IPostProcess`) are never called; an add-on without `ISearch` is refused |

Where the interfaces leave details open, Download Center makes its own choices, which may differ from the original products: for example the names of `Ajax`'s methods (`request`, `get` and `post` are offered), how `addRSSResults` reads a feed (the usual RSS and torrent fields), and the category an add-on is asked for (Download Center's own: `all`, `video`, `audio`, `software`, `books`, `other`). Unlike the original products, certificates of `https` sites are always checked.

### 9.2 What the NAS needs

These plugins run with QTS's built-in PHP, at `/usr/local/apache/bin/php` (it comes with QTS's web server); Download Center brings no PHP of its own and looks nowhere else. Before anything runs, Download Center checks that the NAS can isolate the plugin (section 9.4): at start, each time a file of this kind is checked or installed, and before a search when the last check failed, so turning QTS's web server on later needs no restart. When the check fails, Settings › Search says **This NAS is not compatible with this plugin** with one of these reasons:

- QTS's built-in PHP was not found at /usr/local/apache/bin/php (the page adds: turn on Web Server in QTS Control Panel › Applications, then try again);
- QTS's built-in PHP at /usr/local/apache/bin/php does not start;
- QTS's built-in PHP lacks the curl extension;
- This system cannot isolate programs from the NAS;
- The isolation self-check did not pass.

Plugins of this kind that are installed are then not searched, and their entry in the list shows the reason. Plugin files, Torznab servers and built-in sources are not affected.

### 9.3 Installing: the check and the terms

**Add source** › **Search plugin from another NAS** takes a chosen file only (not an address). Download Center first checks the file without running anything:

- at most 2 MiB; unpacked at most 64 entries and 8 MiB, regular files and folders only, relative names without `..`; no links, devices or other special files;
- a module needs `INFO` (with `type` `search`, `module` naming a file in the package, a valid `class`); an add-on needs `addon.json` (with `class`, `addon` naming a file in the package, an integer `version` from 100, `interface` with `ISearch`, an English `description`) and its key `addon.key`;
- an add-on must carry a valid signature by the key inside it. The signature only shows that the file is intact and which key signed it; it does not tell who wrote the add-on or what it does.

The review then shows the name and version, the format, the sites the plugin declares, whether it signs in with an account, an add-on's key fingerprint, and a warning when it replaces an installed plugin (a new version is installed as a replacement) or when an add-on is signed with another key than the installed one. Below it are the terms, as the page shows them:

> **This plugin runs as a program on this NAS**
>
> - It is a program written by a third party, and it will run on this NAS.
> - Download Center has not reviewed it and is not responsible for what it does, the sites it contacts or what it finds.
> - It runs in a restricted environment that keeps it away from your files and your local network, but no restriction is a guarantee.
> - It can send your search words, and any account you give it, to any site on the internet.
> - Install only plugins from a source you trust.
>
> ☐ I understand and agree; I am responsible for installing it

**Install** works only once the box is ticked. The agreement is tied to the file: the install request carries `"accept_risk": true` and the SHA-256 that the check reported, and is refused (`search_consent_required`) with another file. Who agreed and when is kept and shown in the list. Replacing a plugin is a new installation and needs the terms agreed to again. A new plugin is installed switched off and for administrators only; so is a replacement, which also drops the saved user name and password: the new code gets no account until an administrator enters it again. Only administrators, in the web UI, can install these plugins; when the NAS cannot run them, installing is refused (`search_runtime_unavailable`, with the reason).

The files are kept in `data/search/code/<id>/` (readable by the daemon only), what the package says about itself and the agreement in `data/search/state.json`. Deleting the source deletes its files.

### 9.4 How they run

Each search, test or account check runs the plugin once, in a sandbox of its own:

- It runs as a single process without threads, and may open only TCP and local sockets. It cannot resolve names itself: everything goes through the relay below.
- It has its own network, file system, process, IPC and host name view. It sees a minimal read-only system (what PHP needs), its own files read-only, and a small private temporary folder that is discarded afterwards. Shared folders, the NAS's settings and accounts, Download Center's data and other processes are not there.
- It runs under a user id of no NAS account, without groups, and cannot gain privileges. PHP starts without the system's settings, with only the curl, mbstring, iconv, openssl and zlib extensions, and with the functions that start programs, open network sockets directly, send signals or mail, change the environment or lift the time limit disabled; files can only be opened in the plugin's own folder and the temporary one.
- Each run has a 768 MiB memory limit set by the kernel, not only PHP's own setting, and runs under a system-call filter that refuses what could help it out of the restricted environment. Two runs at once use different user ids. The self-check (section 9.2) confirms all of this, and plugins do not run where it fails.
- Each run may take 30 seconds, after which it is stopped with everything it started ("The plugin did not finish in time"); memory, output, file sizes and processes are limited too. At most two such runs happen at a time; the others wait.
- Its only way out is a relay in Download Center. The relay connects only to public addresses: never to a loopback, link-local, private LAN (RFC 1918, unique local), carrier-grade NAT or reserved address, nor to the NAS's own, and only to ports 80 and 443. A name is refused when any address it resolves to is refused, and the address checked is the one connected to. Each run may open 30 connections and move 16 MiB in all. The relay uses the proxy that the proxy settings choose for a regular user's download of that address, never falling back to a direct connection; with a SOCKS5 proxy that resolves names itself (remote DNS), only literal addresses and `localhost` can be checked; a proxy the plugin sets itself cannot be reached, nor can anything else. `https` traffic passes through as it is, so Download Center sees where it goes, not what it says.

### 9.5 Results, accounts and limits

- What a plugin returns goes through the same checks as any source's results (section 5.2): titles cleaned and cut, magnet links rebuilt from the infohash, only `http` and `https` addresses, at most 100 results, merging by infohash.
- A result's `.torrent` is fetched later by Download Center through the guarded client of section 8, without the plugin and without its cookies. A site that hands out `.torrent` files only to signed-in visitors therefore works only through results that are magnet links.
- A plugin from another NAS that reports 0 seeders and 0 leechers is treated as not knowing them (many write 0 when the site does not say); 0 seeders with leechers stays 0.
- A plugin that signs in has the settings User name and Password. The password is kept like an API key: never shown again, and given only to that plugin when it runs. **Save and verify account** asks the plugin whether the site takes the account.
- Plugins written for older PHP versions may fail with the PHP version of current QTS releases (PHP 8); a plugin that fails, or whose answer is too large or unreadable, is reported as "The plugin failed". Its own error messages are not shown or logged as they are (they may contain the search words).
- The plugin's program can still send the search words and the account it was given to any public site; that is part of the terms.

## 10. Lawful use

Search sources are third-party sites, and their results are what those sites list. The built-in sources are limited to sites of freely distributable content (for the Internet Archive, items whose uploaders marked them with a license, which is the uploader's statement); a source an administrator adds is that administrator's responsibility. Download only what you have the right to download.
