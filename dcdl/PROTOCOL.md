# dc-dl protocol

`dc-dl` is the libcurl transfer engine of Download Center: HTTP, HTTPS
(HTTP/1.1 and HTTP/2), FTP, FTPS, SFTP and SCP. It is started and supervised
by `dcd` (client: `daemon/internal/engine/dl/sidecar.go`), which keeps all
download logic — pieces, control files, connections per task, rate limits —
and asks `dc-dl` for one byte range at a time.

```
dc-dl --socket <data>/run/dc-dl.sock --known-hosts <data>/ssh_known_hosts --ca-file <root>/certs/cacert.pem
```

| Option | Meaning |
|---|---|
| `--socket` | Socket path; created with mode 0600, removed on exit |
| `--known-hosts` | OpenSSH `known_hosts` for SFTP/SCP; created empty when missing. A host seen for the first time is added (trust on first use); a different key later makes the transfer fail |
| `--ca-file` | Certificate authorities (PEM) trusted besides the system's, never instead of them (since 1.3). The system's file (`SSL_CERT_FILE` when set, else the first of `/etc/ssl/certs/ca-certificates.crt`, `/etc/ssl/ca-bundle.crt`, `/etc/pki/tls/certs/ca-bundle.crt`, `/etc/ssl/cert.pem`), the certificates of the system's hashed folder (`SSL_CERT_DIR` when set, else `/etc/ssl/certs`; since 1.5, only its hash-named entries, `<8 hex digits>.<n>`, the ones OpenSSL looks up, without duplicates) and this one are written together into `dc-dl-ca.pem` beside the socket at start (mode 0600 since 1.4: whoever can change it adds authorities), which is then the only CA source: libcurl keeps one parsed store for all connections only when no CA folder is set. Unreadable: the system's file and folder alone (logged) |
| `--prefer-chacha` | Offer ChaCha20-Poly1305 first even when the CPU has AES instructions (for testing; see below) |
| `--version` | Print the versions and exit |

**Every connection is one transfer.** dcd opens a connection, writes one JSON
line, then reads frames until the connection closes. Closing the connection
from dcd's side aborts the transfer. Any number of connections may be open at
once (up to 256); they share one `curl_multi` handle, so connections to the
same server are reused between transfers. HTTP/2 multiplexing is off: dcd
opens several ranges of one file to get several TCP connections, and with
multiplexing they would all share the first one.

Without AES instructions (AES-NI and PCLMULQDQ on x86, the ARMv8 AES and
PMULL extensions on ARM; the 32-bit ARM models have neither) TLS offers
ChaCha20-Poly1305 first, which is several times faster there; the cipher set
stays OpenSSL's default. `version` reports this as `aes_hw`.

## Request

```json
{"url": "https://host/file.iso", "range": "1048576-2097151", "headers": ["Cookie: a=b"],
 "user": "u", "pass": "p", "proxy": "socks5h://user:pass@10.0.0.1:1080", "guard": true, "head": false, "ua": "", "follow": false,
 "chain": ["MIIF…"], "skip_cert_check": false}
```

A request that is not a JSON object, or whose fields have other types than
below, fails (`E`) instead of being guessed at.

| Field | Meaning |
|---|---|
| `url` | `http`, `https`, `ftp`, `ftps`, `sftp` or `scp`; anything else fails |
| `range` | `CURLOPT_RANGE` (`"a-"` or `"a-b"`, inclusive, digits only: anything else fails with `bad range`); omitted = whole file. SCP has no ranges |
| `head` | Only the response headers (`CURLOPT_NOBODY`); for FTP libcurl reports `Content-Length`, `Last-Modified` and `Accept-ranges` as header lines |
| `headers` | Extra HTTP request headers (lines with CR/LF are dropped) |
| `user`, `pass` | Credentials (HTTP Basic sent at once; FTP/SFTP/SCP login). Dropped on redirects to another host |
| `proxy` | `http://`, `socks5://` or `socks5h://` proxy, with credentials in the URL; empty = direct. Environment proxies are never used |
| `guard` | Refuse to connect to loopback, link-local, unspecified, multicast and the NAS's own addresses — checked on every socket (redirects and FTP data connections included) and again before every request, so a connection kept from an earlier transfer without `guard` is checked too (`CURLOPT_PREREQFUNCTION`). Not applied when `proxy` is set: dcd checks the target of such a request (each redirect included) itself |
| `follow` | `false`: HTTP redirects are not followed; the `H` frame carries the redirect (`status` 3xx, `Location` in `headers`) and dcd follows it with a new request, checking the new address and dropping credentials and all but a few headers when the host changes (since 1.2; omitted = `true`, libcurl follows up to 10 redirects) |
| `ua` | User agent (default `DownloadCenter/1.0`) |
| `chain` | Candidate intermediate certificates (base64 DER, at most 8 of at most 16 KiB each; anything else fails with `bad chain`). They join the certificates the server sends as *untrusted* ones, so they can only complete a chain that still has to end at an authority of the CA file; they never become authorities themselves (libcurl treats every certificate of the CA file as one). Not used for SFTP/SCP or with `skip_cert_check` (since 1.5) |
| `skip_cert_check` | `true`: the server's certificate and host name are not checked (HTTPS, FTPS; `CURLOPT_SSL_VERIFYPEER` and `VERIFYHOST` 0). libcurl keeps such connections and TLS sessions apart from verifying ones: the verify flags are part of its connection match and session cache key (since 1.5) |

`{"cmd": "version"}` instead answers one JSON line and closes:
`{"dcdl": "1.5", "curl": "8.22.0", "ssl": "OpenSSL/3.5.9", "libssh": "libssh2/1.11.1", "nghttp2": "1.70.0", "http2": true, "aes_hw": true, "protocols": ["ftp", …], "chain": true, "skip_cert_check": true}`
(`aes_hw` since 1.1; `follow`, the check of kept connections and the limits on response headers since 1.2; `--ca-file` since 1.3; the merged CA file 0600 since 1.4; `chain`, `skip_cert_check`, `need_issuer` and the hashed folder in the CA file since 1.5, reported as `"chain": true, "skip_cert_check": true`).

## Frames

Each frame is a type byte, a 4-byte big-endian length and the payload.

| Type | Payload | When |
|---|---|---|
| `H` | JSON: `status` (HTTP status or last FTP reply code; 0 = none), `headers` (raw lines of the final response, status line first; at most 200 lines of 4096 bytes and 64 KiB in all, longer lines cut, the rest dropped), `url` (effective URL after redirects), `length` (bytes to come, -1 unknown), `filetime` (Unix time, -1 unknown) | Exactly once, before the first `D`, or before `E` when no data came |
| `D` | Data | As it arrives; paused while more than 256 KiB wait for dcd |
| `E` | JSON: `code` (CURLcode; 0 = complete), `error`, `blocked` (refused by `guard`), `hostkey_changed` (SFTP/SCP key differs from `known_hosts`), `need_issuer` (1.5; with code 60 when the chain broke off for want of an issuer: `cert`, the base64 DER of the certificate without one, and `urls`, its caIssuers addresses, at most 4 printable http/https URLs of up to 1024 characters; dcd may fetch the issuer and send the request again with it in `chain`) | Last frame |

HTTP error statuses do not fail the transfer (`code` 0): dcd reads `status`
from `H` and closes the connection itself.

Only `CONNECTTIMEOUT` (30 s) is set; a stalled transfer is noticed by dcd,
which closes the connection (also when no frame at all comes for 60 seconds).
