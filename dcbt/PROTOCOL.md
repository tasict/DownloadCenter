# dc-bt protocol

`dc-bt` is the libtorrent 2.0 torrent engine of Download Center. It is started
and supervised by `dcd` (adapter: `daemon/internal/engine/lt`) and talks to it
over a unix stream socket with one JSON object per line (UTF-8, `\n`
terminated).

```
dc-bt --socket <data>/run/dc-bt.sock --state <data>/bt --torrents <data>/torrents --settings <data>/dc-bt.json
```

| Option | Meaning |
|---|---|
| `--socket` | Socket path; created with mode 0600, removed on exit |
| `--state` | Resume data (`<infohash>.resume`), per-torrent options (`<infohash>.json`) and the session state (`session.state`: DHT nodes) |
| `--torrents` | Where the `.torrent` of a magnet is written once its metadata arrives (`<infohash>.torrent`), so dcd can re-add the task later |
| `--settings` | Initial settings (same object as `apply_settings`), read before the session starts so it never listens on a default port |
| `--check-resume <file.fastresume> <file.torrent>` | Diagnostic: parse an official libtorrent 1.2 resume file against its torrent and print whether it is usable, then exit |

Only one client is served at a time; a new connection replaces the old one.
On start every torrent found in `--state` is restored. `SIGTERM` (or the
`shutdown` command) saves resume data for all torrents and exits.

`<infohash>` is always the v1 info hash in lower-case hex, or the first 40 hex
digits of the v2 hash for v2-only torrents (the same id dcd uses).

## Requests and responses

```json
→ {"id": 7, "cmd": "pause", "infohash": "08ada5a7…"}
← {"id": 7, "ok": true, "result": {}}
← {"id": 7, "ok": false, "error": "torrent not found"}
```

## Events

Lines without `id`, sent as they happen:

```json
{"event": "metadata_received", "infohash": "…"}
{"event": "torrent_finished", "infohash": "…"}
{"event": "seeding_complete", "infohash": "…"}
{"event": "torrent_error", "infohash": "…", "error": "…"}
{"event": "state_changed", "infohash": "…", "state": "downloading"}
{"event": "resume_saved", "infohash": "…"}
{"event": "storage_moved", "infohash": "…", "save_path": "/share/…"}
{"event": "storage_move_failed", "infohash": "…", "error": "…"}
{"event": "listen_failed", "error": "…"}
```

## Commands

| cmd | Arguments | Result |
|---|---|---|
| `version` | — | `{"dcbt": "1.1", "libtorrent": "2.0.15"}` (`move` and `root` need 1.1) |
| `apply_settings` | settings object (below) | `{}` |
| `add` | `torrent_b64` or `magnet`, `save_path`, `select` ([file index], omitted = all), `priorities` ({"index": 0..7}), `trackers` ([url]), `paused`, `seed_ratio` (float, 0 = none), `seed_time` (minutes; -1 = do not seed, 0 = no time limit), `sequential`, `check` (force a recheck), `max_down`, `max_up` (bytes/s), `max_peers`, `fastresume` (path of an official libtorrent 1.2 `.fastresume` to try first), `metadata_only` (fetch the metadata, write `<save_path>/<infohash>.torrent`, then drop the torrent), `root` (the data's top folder, or its single file, is called this instead of the torrent's name) | `{"infohash": "…", "existed": false, "resumed": true}` |
| `pause` / `resume` / `remove` / `force_recheck` | `infohash` | `{}` (`remove` keeps the data and is idempotent) |
| `move` | `infohash`, `save_path`, `root` (optional, renames the top folder or single file on the way) | `{}` at once; the torrent keeps running, status shows `moving` until libtorrent is done, then the new `save_path` (resume data saved right away), or `move_error`. Nothing at the destination is replaced: if a file exists there the move fails and the data stays |
| `set_file_priorities` | `infohash`, `select`, `priorities` | `{}` |
| `set_limits` | `infohash`, `down`, `up` (bytes/s, 0 = none) | `{}` |
| `set_sequential` | `infohash`, `on` | `{}` |
| `set_seeding` | `infohash`, `seed_ratio`, `seed_time` | `{}` |
| `add_trackers` | `infohash`, `trackers` | `{}` |
| `status_all` | `files` (bool, default true) | `{"torrents": [status…]}` |
| `status` | `infohash` | status |
| `peers` | `infohash` | `{"peers": [{"ip","port","client","down_rate","up_rate","seed","progress"}]}` |
| `trackers` | `infohash` | `{"trackers": [url]}` |
| `save_state` | — | `{"saved": n}` once every resume file is written (10 s max) |
| `shutdown` | — | `{}`, then the process saves its state and exits |

### Status object

```json
{
  "infohash": "…", "name": "Sintel", "save_path": "/share/…",
  "state": "checking_files|checking_resume_data|downloading_metadata|downloading|finished|seeding",
  "paused": false, "complete": false, "moving": false, "move_error": "", "has_metadata": true, "error": "",
  "total_wanted": 0, "total_wanted_done": 0, "all_time_upload": 0, "all_time_download": 0,
  "down_rate": 0, "up_rate": 0, "peers": 0, "seeds": 0,
  "pieces": "ff80…", "num_pieces": 987, "piece_length": 131072,
  "sequential": false, "comment": "",
  "files": [{"index": 0, "path": "Sintel/Sintel.mp4", "size": 1, "done": 0, "priority": 4, "pad": false}]
}
```

`pieces` is the have-bitfield in hex, most significant bit first (piece 0 is
the top bit of the first digit), the same layout aria2 uses. `complete` is set
when the torrent reached its seeding target (`seed_ratio` / `seed_time`): dc-bt
pauses it and keeps reporting it as complete, because libtorrent's own ratio
limit is session wide.

### Settings object

```json
{
  "listen_from": 16891, "listen_to": 16899,
  "dht": true, "lsd": true, "pex": true, "upnp": false, "encrypt": false,
  "connections_limit": 300, "max_down": 0, "max_up": 0,
  "peer_id_prefix": "-DC1000-", "user_agent": "",
  "proxy": {"type": "none|http|socks5", "host": "", "port": 0, "user": "", "pass": "",
            "hostnames": true, "peers": true, "trackers": true, "force": true}
}
```

With `proxy.force`, dc-bt never connects directly: incoming connections,
UPnP/NAT-PMP, LSD and uTP are turned off, and DHT is turned off as well
(UDP may not be relayed by the proxy).
