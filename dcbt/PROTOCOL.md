# dc-bt protocol

`dc-bt` is the libtorrent 2.0 torrent engine of Download Center. It is started
and supervised by `dcd` (adapter: `daemon/internal/engine/lt`) and talks to it
over a unix stream socket with one JSON object per line (UTF-8, `\n`
terminated).

```
dc-bt --socket <data>/run/dc-bt.sock --state <data>/bt --torrents <data>/torrents --settings <data>/dc-bt.json --ca-file <root>/certs/cacert.pem
```

| Option | Meaning |
|---|---|
| `--socket` | Socket path; created with mode 0600, removed on exit |
| `--state` | Resume data (`<infohash>.resume`), per-torrent options (`<infohash>.json`) and the session state (`session.state`: DHT nodes) |
| `--torrents` | Where the `.torrent` of a magnet is written once its metadata arrives (`<infohash>.torrent`), so dcd can re-add the task later |
| `--settings` | Initial settings (same object as `apply_settings`), read before the session starts so it never listens on a default port |
| `--ca-file` | Certificate authorities (PEM) trusted for HTTPS trackers and web seeds besides the system's, never instead of them: the system's default file (`SSL_CERT_FILE`, else OpenSSL's built-in `/etc/ssl/cert.pem`) and this one are written together into `<state>/ca.pem` (mode 0600), which becomes the default file; libtorrent also loads `/etc/ssl/certs/ca-certificates.crt`, `/etc/pki/tls/certs/ca-bundle.crt` and the hashed folder (`SSL_CERT_DIR`, else `/etc/ssl/certs`). Unreadable: the system's alone (logged). Since 1.6 |
| `--check-resume <file.fastresume> <file.torrent>` | Diagnostic: parse an official libtorrent 1.2 resume file against its torrent and print whether it is usable, then exit |

Only one client is served at a time; a new connection replaces the old one.
dc-bt never waits for the client: what it has not read yet is kept, and a
client that leaves 256 MiB unread is disconnected. On start every torrent
found in `--state` is restored, except one whose options file cannot be read
or does not say whether its destination is to be treated as hostile (saved
before 1.5), and an `untrusted_dir` torrent whose save path is not a private
folder (see
Regular users' torrents): those wait until dcd adds them again. `SIGTERM` (or
the `shutdown` command) saves resume data for all torrents and exits.

`<infohash>` is always the v1 info hash in lower-case hex, or the first 40 hex
digits of the v2 hash for v2-only torrents (the same id dcd uses). A command
whose `infohash` is anything else answers `torrent not found`.

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

### Low memory profile

Since 1.8 the settings carry `memory_profile`: dcd sends `"low"` on NAS
models with 1.5 GB of memory or less (or when `data/memory_profile` says so),
`"normal"` otherwise. With `"low"` dc-bt uses the values below, taken from
libtorrent's `min_memory_usage()` where they save memory without breaking
anything dc-bt relies on; with anything else (and in 1.7 and older) the
session has libtorrent's defaults for them, as before. `version` and the
start line of the log report the profile (`memory_profile`), the values of
the low profile (`low_memory`) and what the session has now (`session`).

| Setting | `low` | libtorrent default |
|---|---|---|
| `max_peer_recv_buffer_size` | 256 KiB | 2 MiB |
| `send_buffer_watermark` | 128 KiB | 500 KiB |
| `recv_socket_buffer_size`, `send_socket_buffer_size` | 64 KiB | the system's |
| `max_peerlist_size`, `max_paused_peerlist_size` | 500, 50 | 3000, 1000 |
| `file_pool_size` | 10 | 40 |
| `max_out_request_queue`, `max_allowed_in_request_queue` | 300, 250 | 500, 2000 |
| `connection_speed` | 10 | 30 |
| `whole_pieces_threshold`, `prioritize_partial_pieces` | 2, true | 20, false |
| `active_checking` | 1 | 2 (dc-bt) |
| `alert_queue_size` | 2000 | 10000 (dc-bt) |
| `max_queued_disk_bytes`, `checking_mem_usage` | at most 1 MiB, 256 | the hardware's (above) |

Not taken from `min_memory_usage()`: its alert queue of 100 (resume data and
moves would be lost), its one-block disk queue and send buffer (no throughput
left), `disable_os_cache` (seeding would read every block from disk) and 5
connection attempts a second.

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
| `version` | — | `{"dcbt": "1.8", "libtorrent": "2.0.15", "hardware": {"cores": 4, "ram_mb": 31891, "disk_io": "mmap", "hashing_threads": 2, "max_queued_disk_bytes": 16777216, "checking_mem_usage": 2048, "memory_profile": "normal", "session": {…}}}` (`move` and `root` need 1.1; `hardware` since 1.2, see Tuning; `refresh_peers`, `set_trackers`, `set_guard`, `sources`, `guard`, the peer counts and the source fields since 1.4; `untrusted_dir` since 1.5; `--ca-file` and the tracker messages of Sources as described since 1.6; `adopt_from` since 1.7 (1.6 took files over on weaker checks: dcd offers data to 1.7 and newer only); `memory_profile` and the `changed` answers of `status_all` since 1.8) |
| `apply_settings` | settings object (below) | `{}` |
| `add` | `torrent_b64` or `magnet`, `save_path`, `select` ([file index], omitted = all), `priorities` ({"index": 0..7}), `trackers` ([url]), `paused`, `seed_ratio` (float, 0 = none), `seed_time` (minutes; -1 = do not seed, 0 = no time limit), `sequential`, `check` (force a recheck), `max_down`, `max_up` (bytes/s), `max_peers`, `fastresume` (path of an official libtorrent 1.2 `.fastresume` to try first), `metadata_only` (fetch the metadata, write `<save_path>/<infohash>.torrent`, then drop the torrent), `root` (the data's top folder, or its single file, is called this instead of the torrent's name), `guard` (a regular user's torrent: see Address guard; kept with the torrent, a `metadata_only` add never changes it on a torrent that is already there), `untrusted_dir` (the destination folder is treated as one other accounts can change; dcd sends it for every torrent: see Regular users' torrents; omitted = false), `owner_uid` and `owner_gid` (with `untrusted_dir`: the account the handed-over files belong to; omitted = the owner of the folder they are handed to), `adopt_from`, `adopt_root` and `adopt_required` (with `untrusted_dir`: the torrent's data may already sit in `adopt_from`/`adopt_root`, see Data already in the destination) | `{"infohash": "…", "existed": false, "resumed": true, "adopted": n}` (`adopted`: files taken over, see below); for `untrusted_dir` also `{"infohash": "…", "seed_done": true, "total_wanted": n}`: not started (see Regular users' torrents) |
| `pause` / `resume` / `remove` / `force_recheck` | `infohash` | `{}` (`remove` keeps the data, except the private folder an `untrusted_dir` torrent seeded from after its hand-over or take-over, and is idempotent) |
| `move` | `infohash`, `save_path`, `root` (optional, renames the top folder or single file on the way) | `{}` at once; the torrent keeps running, status shows `moving` until libtorrent is done, then the new `save_path` (resume data saved right away), or `move_error`. Nothing at the destination is replaced: if a file exists there the move fails and the data stays. An `untrusted_dir` torrent is handed over instead (see Regular users' torrents) |
| `set_file_priorities` | `infohash`, `select`, `priorities` | `{}` |
| `set_limits` | `infohash`, `down`, `up` (bytes/s, 0 = none) | `{}` |
| `set_sequential` | `infohash`, `on` | `{}` |
| `set_seeding` | `infohash`, `seed_ratio`, `seed_time` | `{}` |
| `add_trackers` | `infohash`, `trackers` | `{}`, then announces to them |
| `set_trackers` | `infohash`, `trackers` | `{}`: the torrent's trackers are replaced by these (in this order), the saved `.torrent` and resume data are rewritten |
| `set_guard` | `infohash`, `on` | `{}`: turns the address guard of the torrent on or off (see Address guard) |
| `refresh_peers` | `infohash` | `{"refreshed": true}`: the torrent had no connection, so its peer list was cleared and it asked DHT (not for private torrents) and its trackers for peers again; `{"refreshed": false}` when it has a connection (nothing changed). libtorrent never tries a peer again after three failed connections while the torrent runs; this is how dcd makes it try again |
| `status_all` | `files` (bool, default true), `changed`, `full` (bool, 1.8) | `{"torrents": [status…]}`; with `changed`: `{"torrents": [status…], "present": ["infohash", …], "full": false}`, see Changed answers |
| `status` | `infohash` | status |
| `peers` | `infohash` | `{"peers": [{"ip","port","client","down_rate","up_rate","seed","progress"}]}` |
| `trackers` | `infohash` | `{"trackers": [url]}` |
| `sources` | `infohash` | `{"trackers": [{"url", "tier", "contacted", "fails", "message", "seeds", "leechers", "updating"}]}`, see Sources |
| `save_state` | — | `{"saved": n}` once every resume file is written (10 s max) |
| `shutdown` | — | `{}`, then the process saves its state and exits |

### Changed answers

Polling every torrent every second costs one status call per torrent in
libtorrent's network thread (where the 32-bit back end also reads, writes and
hashes) and a JSON object per torrent, also for torrents that only seed
without peers. Since 1.8 `status_all` with `"changed": true` answers only
what changed since its previous changed answer to the connected client:

- dc-bt asks libtorrent which torrents changed (`post_torrent_updates`;
  libtorrent answers at once, also when nothing changed) and waits up to a
  second for the answer while handling other alerts. A torrent counts as
  changed when libtorrent says so (its state, progress, a transfer rate above
  0, and the update that brings a rate back to 0), when an alert was about it,
  or when what dc-bt keeps of it (complete, handed over, move error) changed.
  Peer counts and the source fields alone do not make a torrent changed.
- `present` lists every torrent (without metadata-only probes), so that the
  client can forget those that are gone.
- `pieces` is left out when as many pieces are done as in the client's
  last answer; `pieces_done` (since 1.8, in every status) is that number.
- With `files`, only the changed torrents carry their file list.
- The first changed request of a connection, one with `"full": true` and one
  whose libtorrent answer does not come in time get every torrent with
  `"full": true` (pieces and, with `files`, file lists included).

Without `changed` (dc-bt 1.7 and older, and dcd's `StatusAll`), every
torrent is answered and the changed answers are left as they were. dcd asks
for a full answer every 60 seconds and keeps the last status of the others.

### Status object

```json
{
  "infohash": "…", "name": "Sintel", "save_path": "/share/…",
  "state": "checking_files|checking_resume_data|downloading_metadata|downloading|finished|seeding",
  "paused": false, "complete": false, "moving": false, "move_error": "", "has_metadata": true, "error": "",
  "total_wanted": 0, "total_wanted_done": 0, "all_time_upload": 0, "all_time_download": 0,
  "down_rate": 0, "up_rate": 0, "peers": 0, "seeds": 0, "known_peers": 0, "connect_candidates": 0,
  "availability": 0.82, "last_seen_complete": 0, "found_peers": 0, "dht_up": true, "dht_peers": 0, "dht_reply_at": 0,
  "pieces": "ff80…", "pieces_done": 412, "num_pieces": 987, "piece_length": 131072,
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
torrent`, or `certificate verify failed` for an HTTPS tracker whose
certificate the trusted authorities do not vouch for; the error otherwise) or
its warning when it works. Entries of listen sockets that cannot reach the
tracker's address at all (loopback, a bridge without a route) skip the
announce (`skipping tracker announce (unreachable)`): they count only when
every entry did, else the other entries' failures and message are reported; `seeds` and
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
  "memory_profile": "normal|low",
  "proxy": {"type": "none|http|socks5", "host": "", "port": 0, "user": "", "pass": "",
            "hostnames": true, "peers": true, "trackers": true, "force": true}
}
```

With `proxy.force`, dc-bt never connects directly: incoming connections,
UPnP/NAT-PMP, LSD and uTP are turned off, and DHT is turned off as well
(UDP may not be relayed by the proxy). Trackers and web seeds go through the
proxy whatever `trackers` and `peers` say; peers go through it when it can
carry them (`socks5` with `peers`), and otherwise (an `http` proxy, or
`peers` false) dc-bt connects to no peer at all. With `force` but no `host`
(or an unknown `type`) every connection goes to a closed port on the NAS
itself, so nothing goes out.

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

Web seeds of a guarded torrent given by name are looked up when they are
added (away from the loop); one whose name leads to the NAS's addresses, or
to those of the tracker proxy the filter lets through, is removed. Peers at
the tracker proxy's addresses are still let through the filter, at any port.

## Regular users' torrents

dc-bt runs as root, and a regular user can put links (symbolic or hard) into
their own folders at any time. A torrent added with `untrusted_dir` (dcd
sends it for every torrent, administrators' too) is therefore only ever read
and written in folders only root can change:

- Its `save_path` must be a private folder: a real folder (reached without a
  link other than root's own links in root's folders), owned by root and not
  writable by group or others, in a folder of the same kind. dcd makes it.
  Otherwise the add fails with `not a private folder: …`, unless the torrent's
  saved resume data has every wanted piece (a torrent of an earlier version
  that seeds from the user's folder, or one dcd believes is in the user's
  folder): then it is not started and the answer says `seed_done` and
  `total_wanted`; dcd counts its seeding as finished. Nothing is read there.
- `move` hands the data over: every complete, wanted file appears in
  `save_path` (the user's folder, which must be on the same volume) as a hard
  link of the file in the private folder, the top folder (or single file)
  named `root`. `save_path` is opened from `/` one folder at a time without
  following a link (except root's own links in root's folders, such as
  `/share/homes`), and everything below it is made with `mkdirat`/`linkat`
  from those folders: nothing that exists is replaced or followed. The folders
  made belong to root (0700) until they are complete, then to `owner_uid`
  (or the owner of `save_path`), 0775, and so do the linked files. A name that is taken fails
  the move (`File exists: …`). Then the torrent moves (inside root's folders)
  to `<private folder>.seed` beside the private folder, made 0700, and seeds
  from there: dcd may delete the private folder once the move is reported
  done. Status then reports the user's folder as `save_path` and the file
  paths with `root`.
- Files chosen later download into the seed folder and are handed over, in
  the same way, when the torrent finishes again; a name the user has taken
  meanwhile is left as it is (logged).
- `remove` deletes the seed folder; the user's hard links keep the data.
- The hand-over fails (`the destination changed while the files were handed
  over: …`) when, at its end, the top folder (or single file) it made in
  `save_path` is no longer there under its name (renamed meanwhile, with
  something else put in its place).
- An already running torrent that dcd adds again with `untrusted_dir` keeps
  running when it runs from a private folder (and moves to another private
  `save_path` dcd gives, checking the data there); one that runs from
  anywhere else stops and is treated as added anew.

### Data already in the destination

With `adopt_from` (a folder, opened like a hand-over destination) and
`adopt_root` (the name of the top folder or single file there; omitted = the
torrent's name), an `add` that starts the torrent anew in its private
`save_path` first takes over what of the torrent's data is already there:

- Each wanted file of the torrent found there is opened without following a
  link and must be a regular file with a single name (nlink 1, or 2 when the
  other name is the private copy itself, from an earlier take-over or
  hand-over) that belongs to `owner_uid` (without `owner_uid`: the owner of
  `adopt_from`, or root) and is no larger than the torrent's file.
- Its data must be the torrent's, checked against the piece hashes (SHA-1)
  before anything is linked: every piece the file holds whole matches,
  and at least one of them is not a run of a single byte value. A piece
  counts when all its data is there (judged from the file sizes before
  anything is read): one that reaches past the end of a file, or into a
  file that is not there, counts neither way (libtorrent writes it later,
  also the part in the user's file). Pieces are read in order, each at most
  once, and what one shows counts for every file it covers; files not
  wanted are read, never taken over, to complete the pieces they share with
  wanted ones, and count like them. So a file that only shares a name or a
  block (zeros, say) with the torrent's is never written to, and neither is
  a copy the user changed since.
- All or nothing: if a file of the torrent is there but does not qualify,
  holds a piece that does not match, or changes while it is checked,
  nothing is taken over. A wanted file without a piece that tells it by its
  data (no piece held whole, or only runs of one byte value) is left out
  (downloaded privately; the user's file is left alone). A check gives up
  (nothing is taken over) after reading 2 TiB, opening 2 million files or
  six hours.
- Each file is found as it was checked by its device and inode number, link
  count, size, modification and change times and, where the file system
  has one, the inode's generation (`FS_IOC_GETVERSION`): a file deleted and
  made anew under the same name, which may get the same inode number,
  differs in its generation and change time; one that changed in any other
  way differs in its times or links. Every time the check opens a file
  again, and when it is linked, it is opened without following a link and
  compared; it is then hard-linked into the private folder from that
  descriptor (`linkat` with `AT_EMPTY_PATH`, or `/proc/self/fd`), never by
  name. If one no longer is what was checked, nothing is taken over.
- The torrent is then checked again by libtorrent: only pieces that verify
  count (and are ever sent), missing pieces are downloaded into the private
  copy, which for a file taken over is the user's file (a hard link the
  user makes to it later only gives them torrent data). Files that were not
  there are downloaded privately and handed over when the torrent finishes,
  beside the others.
- The torrent then counts as handed over to `adopt_from`/`adopt_root` and
  seeds from `save_path` (which `remove` deletes). Torrents without SHA-1
  piece hashes (v2 only) and magnets without a file list take nothing over.
- The checks read and hash data, so they run in the background, in threads
  named `dc-bt adopt`: at most two at a time, at most one per `owner_uid`
  (one for all torrents without it). Until they are done, `add` answers
  `{"infohash": "…", "adopting": true}` without adding anything, and dcd
  adds again later; the add that finds them done links what qualified and
  adds the torrent. `remove`, or an `add` that names another
  `adopt_from`/`adopt_root`, stops a running check; its thread keeps its
  slot until it has stopped. A file the private folder already holds under
  another inode is left alone (the private copy is used).
- With `adopt_required` the add fails (`cannot take over the data: …`) when
  nothing is taken over; without it the torrent downloads as new.
