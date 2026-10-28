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

## Tuning

Some session settings follow the hardware, detected once at start (online
cores, `MemTotal`) and reported by `version` and in the start line of the log.
TCP peers are never throttled in favour of uTP (`mixed_mode_algorithm` =
`prefer_tcp`). libtorrent 2.0 uses memory-mapped disk I/O on 64-bit
(`disk_io` = `mmap`) and a single-threaded posix back end on 32-bit
(`posix`); the disk settings only matter for `mmap`, and are left at
libtorrent's defaults otherwise.

| Setting | `mmap` value | libtorrent default |
|---|---|---|
| `hashing_threads` | half the cores, 1–4 | 1 |
| `max_queued_disk_bytes` | 1 MiB below 1 GB RAM, 2 MiB from 1 GB, 4 MiB from 2 GB, 8 MiB from 4 GB, 16 MiB from 8 GB | 1 MiB |
| `checking_mem_usage` (16 KiB blocks) | 256, 512 from 2 GB, 1024 from 4 GB, 2048 from 8 GB | 256 |

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
| `version` | — | `{"dcbt": "1.4", "libtorrent": "2.0.15", "hardware": {"cores": 4, "ram_mb": 31891, "disk_io": "mmap", "hashing_threads": 2, "max_queued_disk_bytes": 16777216, "checking_mem_usage": 2048}}` (`move` and `root` need 1.1; `hardware` since 1.2, see Tuning; `refresh_peers`, `set_trackers`, `set_guard`, `sources`, `guard`, the peer counts and the source fields since 1.4) |
| `apply_settings` | settings object (below) | `{}` |
| `add` | `torrent_b64` or `magnet`, `save_path`, `select` ([file index], omitted = all), `priorities` ({"index": 0..7}), `trackers` ([url]), `paused`, `seed_ratio` (float, 0 = none), `seed_time` (minutes; -1 = do not seed, 0 = no time limit), `sequential`, `check` (force a recheck), `max_down`, `max_up` (bytes/s), `max_peers`, `fastresume` (path of an official libtorrent 1.2 `.fastresume` to try first), `metadata_only` (fetch the metadata, write `<save_path>/<infohash>.torrent`, then drop the torrent), `root` (the data's top folder, or its single file, is called this instead of the torrent's name), `guard` (a regular user's torrent: see Address guard; kept with the torrent, a `metadata_only` add never changes it on a torrent that is already there) | `{"infohash": "…", "existed": false, "resumed": true}` |
| `pause` / `resume` / `remove` / `force_recheck` | `infohash` | `{}` (`remove` keeps the data and is idempotent) |
| `move` | `infohash`, `save_path`, `root` (optional, renames the top folder or single file on the way) | `{}` at once; the torrent keeps running, status shows `moving` until libtorrent is done, then the new `save_path` (resume data saved right away), or `move_error`. Nothing at the destination is replaced: if a file exists there the move fails and the data stays |
| `set_file_priorities` | `infohash`, `select`, `priorities` | `{}` |
| `set_limits` | `infohash`, `down`, `up` (bytes/s, 0 = none) | `{}` |
| `set_sequential` | `infohash`, `on` | `{}` |
| `set_seeding` | `infohash`, `seed_ratio`, `seed_time` | `{}` |
| `add_trackers` | `infohash`, `trackers` | `{}`, then announces to them |
| `set_trackers` | `infohash`, `trackers` | `{}`: the torrent's trackers are replaced by these (in this order), the saved `.torrent` and resume data are rewritten |
| `set_guard` | `infohash`, `on` | `{}`: turns the address guard of the torrent on or off (see Address guard) |
| `refresh_peers` | `infohash` | `{"refreshed": true}`: the torrent had no connection, so its peer list was cleared and it asked DHT (not for private torrents) and its trackers for peers again; `{"refreshed": false}` when it has a connection (nothing changed). libtorrent never tries a peer again after three failed connections while the torrent runs; this is how dcd makes it try again |
| `status_all` | `files` (bool, default true) | `{"torrents": [status…]}` |
| `status` | `infohash` | status |
| `peers` | `infohash` | `{"peers": [{"ip","port","client","down_rate","up_rate","seed","progress"}]}` |
| `trackers` | `infohash` | `{"trackers": [url]}` |
| `sources` | `infohash` | `{"trackers": [{"url", "tier", "contacted", "fails", "message", "seeds", "leechers", "updating"}]}`, see Sources |
| `save_state` | — | `{"saved": n}` once every resume file is written (10 s max) |
| `shutdown` | — | `{}`, then the process saves its state and exits |

### Status object

```json
{
  "infohash": "…", "name": "Sintel", "save_path": "/share/…",
  "state": "checking_files|checking_resume_data|downloading_metadata|downloading|finished|seeding",
  "paused": false, "complete": false, "moving": false, "move_error": "", "has_metadata": true, "error": "",
  "total_wanted": 0, "total_wanted_done": 0, "all_time_upload": 0, "all_time_download": 0,
  "down_rate": 0, "up_rate": 0, "peers": 0, "seeds": 0, "known_peers": 0, "connect_candidates": 0,
  "availability": 0.82, "last_seen_complete": 0, "found_peers": 0, "dht_up": true, "dht_peers": 0, "dht_reply_at": 0,
  "pieces": "ff80…", "num_pieces": 987, "piece_length": 131072,
  "sequential": false, "comment": "", "private": false,
  "files": [{"index": 0, "path": "Sintel/Sintel.mp4", "size": 1, "done": 0, "priority": 4, "pad": false}]
}
```

`pieces` is the have-bitfield in hex, most significant bit first (piece 0 is
the top bit of the first digit), the same layout aria2 uses. `complete` is set
when the torrent reached its seeding target (`seed_ratio` / `seed_time`): dc-bt
pauses it and keeps reporting it as complete, because libtorrent's own ratio
limit is session wide. `peers` and `seeds` count connected peers; `known_peers` is
every peer the torrent knows of (connected or not, including those it gave up
on) and `connect_candidates` those it would still try. `private` (the info
dictionary's `private` flag) is only there once the metadata is.

`availability` is how many complete copies the connected peers have together
(libtorrent's `distributed_copies`: 0.82 means 82% of the pieces are there at
least once), -1 without metadata and once the torrent is complete.
`last_seen_complete` is when the torrent or one of its peers last had every
piece (Unix time, 0 unknown). `found_peers` counts the distinct peers DHT,
trackers, peer exchange and the like handed to the torrent since dc-bt started,
without the NAS itself: DHT returns our own announce (some nodes under another
address, with our port), and trackers return the addresses we announced from
(IPv4, IPv6, each interface) to one another. Self is an address of the
interfaces or one libtorrent learnt as external (`external_ip_alert`), or our
listen port at any address, so another client behind the same NAT, or one on
the same port (Download Center's default), is not counted either (libtorrent
still connects to it); peers the IP filter refused and those from resume data
do not count. Addresses learnt later are taken out of the counts. A torrent plugin sees every peer as libtorrent adds it
(`on_add_peer`). `known_peers` is libtorrent's own count, self included.
`dht_up` says the torrent is being announced to DHT: DHT is on, had nodes at
the last check (see DHT bootstrap retry) and the torrent is not private;
libtorrent then asks DHT for its peers every 15 minutes. `dht_peers` is how
many of the peers found came from DHT and `dht_reply_at` when DHT last handed
one over (Unix time, 0 never). libtorrent reports nothing for a lookup that
finds nobody, so "DHT found nobody" means `dht_up` for a while without a newer
`dht_reply_at`.

### Sources

`sources` reports what libtorrent knows of each tracker from its announces; it
reads memory and sends nothing. A tracker has an endpoint per listen socket and
an entry per info hash (v1, v2), which dc-bt sums up: `contacted` is true
once an announce was answered or failed; `fails` is the fewest failures in a
row among the entries tried (0 means it answered the last time); `message` is
the tracker's failure reason when it fails (for example `unregistered
torrent`; the error otherwise) or its warning when it works; `seeds` and
`leechers` are the largest counts any entry reported (from announces, which
carry them; -1 unknown; the NAS itself is among the leechers once it
announced, maybe more than once with several addresses); `updating` means an announce is under way.

### Settings object

```json
{
  "listen_from": 16891, "listen_to": 16899,
  "dht": true, "lsd": true, "pex": true, "upnp": false, "encrypt": false,
  "connections_limit": 300, "max_down": 0, "max_up": 0,
  "peer_id_prefix": "-DC1000-", "user_agent": "", "dht_bootstrap_nodes": "host:port,…",
  "proxy": {"type": "none|http|socks5", "host": "", "port": 0, "user": "", "pass": "",
            "hostnames": true, "peers": true, "trackers": true, "force": true}
}
```

With `proxy.force`, dc-bt never connects directly: incoming connections,
UPnP/NAT-PMP, LSD and uTP are turned off, and DHT is turned off as well
(UDP may not be relayed by the proxy).

`dht_bootstrap_nodes` is for tests; dcd does not send it and the built-in list
of public routers applies. dc-bt announces to every tracker of a torrent and
every tier together (`announce_to_all_trackers`, `announce_to_all_tiers`), so
trackers added later are used right away instead of after the others failed.

### DHT bootstrap retry

libtorrent looks up the DHT bootstrap nodes once, when DHT starts, and never
again when that fails (no network yet when the NAS boots, and no nodes saved in
`session.state`). Two minutes after start, and every 5 minutes after that,
dc-bt asks the session how many DHT nodes it has; with DHT on and no nodes it
turns DHT off and on again, which looks the bootstrap nodes up again, and logs
it. While there are still no nodes the checks are 5, 10, 20 and then 30 minutes
apart; with nodes they go back to every 5 minutes. When a check finds nodes for
the first time, or again after retries, every running torrent without a
connection asks DHT for peers right away: torrents added or resumed before DHT
had nodes found nobody, and libtorrent would ask again only on its round, up
to 15 minutes later.

### Address guard

Torrents added with `guard` (dcd sets it for regular users' torrents, for
their magnet probes and for every torrent that has the public tracker list)
never connect to loopback, link-local, unspecified or multicast addresses or
to the NAS's own addresses (also as IPv4-mapped IPv6), the same rule as
dc-dl's `guard`; other LAN hosts are fine. The session IP filter blocks these
addresses (the NAS's addresses are read again every minute) and only guarded
torrents apply it, so trackers, web seeds and peers are checked after their
names resolve. Trackers and web seeds of a guarded torrent whose host is such
an address written out (in any form the C library takes as an address, such
as `2130706433` or `0x7f.1`) or `localhost` (`*.localhost`) are also removed
when they are added (`add`, `add_trackers`, `set_trackers`, `set_guard`): a
proxy that resolves names itself would otherwise reach them.

The filter stays on with a proxy. libtorrent checks a SOCKS5 connection by
its target (or cannot, when the proxy resolves the name), but an HTTP proxy
for trackers by the proxy's own address: when that proxy runs on the NAS
itself, its addresses are let through the filter (a name is looked up away
from the loop, and again every 10 minutes). What a proxy reaches is up to the
proxy. Web seeds and peers given by name are checked against the filter by
libtorrent even for torrents without `guard`.

`set_guard` (`infohash`, `on`) changes the guard of a torrent that is already
there: dcd sends it when its owner becomes or stops being an administrator,
or when it gets the public tracker list. The guard is saved with the torrent.
A torrent saved before 1.4 has no guard in its options: it is restored paused
and runs once dcd has added it again and said whose it is.
