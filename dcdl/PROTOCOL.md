# dc-dl protocol

`dc-dl` is the libcurl transfer engine of Download Center: HTTP, HTTPS
(HTTP/1.1 and HTTP/2), FTP, FTPS, SFTP and SCP. It is started and supervised
by `dcd` (client: `daemon/internal/engine/dl/sidecar.go`), which keeps all
download logic — pieces, control files, connections per task, rate limits —
and asks `dc-dl` for one byte range at a time.

```
dc-dl --socket <data>/run/dc-dl.sock --known-hosts <data>/ssh_known_hosts
```

| Option | Meaning |
|---|---|
| `--socket` | Socket path; created with mode 0600, removed on exit |
| `--known-hosts` | OpenSSH `known_hosts` for SFTP/SCP; created empty when missing. A host seen for the first time is added (trust on first use); a different key later makes the transfer fail |
| `--version` | Print the versions and exit |

**Every connection is one transfer.** dcd opens a connection, writes one JSON
line, then reads frames until the connection closes. Closing the connection
from dcd's side aborts the transfer. Any number of connections may be open at
once (up to 256); they share one `curl_multi` handle, so connections to the
same server are reused between transfers.

## Request

```json
{"url": "https://host/file.iso", "range": "1048576-2097151", "headers": ["Cookie: a=b"],
 "user": "u", "pass": "p", "proxy": "socks5h://user:pass@10.0.0.1:1080", "guard": true, "head": false, "ua": ""}
```

| Field | Meaning |
|---|---|
| `url` | `http`, `https`, `ftp`, `ftps`, `sftp` or `scp`; anything else fails |
| `range` | `CURLOPT_RANGE` (`"a-"` or `"a-b"`, inclusive); omitted = whole file. SCP has no ranges |
| `head` | Only the response headers (`CURLOPT_NOBODY`); for FTP libcurl reports `Content-Length`, `Last-Modified` and `Accept-ranges` as header lines |
| `headers` | Extra HTTP request headers (lines with CR/LF are dropped) |
| `user`, `pass` | Credentials (HTTP Basic sent at once; FTP/SFTP/SCP login). Dropped on redirects to another host |
| `proxy` | `http://`, `socks5://` or `socks5h://` proxy, with credentials in the URL; empty = direct. Environment proxies are never used |
| `guard` | Refuse to connect to loopback, link-local, unspecified, multicast and the NAS's own addresses — checked on every socket (redirects and FTP data connections included). Not applied when `proxy` is set |
| `ua` | User agent (default `DownloadCenter/1.0`) |

`{"cmd": "version"}` instead answers one JSON line and closes:
`{"dcdl": "1.0", "curl": "8.22.0", "ssl": "OpenSSL/3.5.9", "libssh": "libssh2/1.11.1", "nghttp2": "1.70.0", "http2": true, "protocols": ["ftp", …]}`.

## Frames

Each frame is a type byte, a 4-byte big-endian length and the payload.

| Type | Payload | When |
|---|---|---|
| `H` | JSON: `status` (HTTP status or last FTP reply code; 0 = none), `headers` (raw lines of the final response, status line first), `url` (effective URL after redirects), `length` (bytes to come, -1 unknown), `filetime` (Unix time, -1 unknown) | Exactly once, before the first `D`, or before `E` when no data came |
| `D` | Data | As it arrives; paused while more than 256 KiB wait for dcd |
| `E` | JSON: `code` (CURLcode; 0 = complete), `error`, `blocked` (refused by `guard`), `hostkey_changed` (SFTP/SCP key differs from `known_hosts`) | Last frame |

HTTP error statuses do not fail the transfer (`code` 0): dcd reads `status`
from `H` and closes the connection itself.

Only `CONNECTTIMEOUT` (30 s) is set; a stalled transfer is noticed by dcd,
which closes the connection.
