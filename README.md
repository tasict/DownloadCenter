# Download Center

A download manager for QNAP NAS running QTS. It downloads HTTP, HTTPS, FTP, FTPS, SFTP and SCP links, `.torrent` files and magnet links, and is an independent alternative to QNAP's Download Station.

Download Center is not affiliated with, endorsed by or supported by QNAP Systems, Inc.

## Features

- **Links and torrents in one list.** Paste one link, many links, a block of text or a web page address; drop `.torrent` files on the window. Magnet links show their file list before you start.
- **Two engines, each for what it does best.** libcurl for URLs (up to four connections per file, resume after restarts, HTTP/2) and libtorrent 2.0 for BitTorrent.
- **Queues and limits.** Per-type concurrency and speed limits, a weekly schedule with full speed, limited and paused hours, timed pause, seeding by ratio or time.
- **Where files go.** A temporary location and an optional "move when finished" folder, like Download Station. Regular users always download into their own home folder and own their files.
- **Users and roles.** Sign in with your NAS account. Administrators see everything; regular users see only their own downloads.
- **Proxies.** HTTP and SOCKS5 proxies for URL downloads and torrents, chosen per task or per site, with remote DNS.
- **File-hosting accounts.** 1fichier, Rapidgator, Real-Debrid and AllDebrid through their official APIs with your own account, or cookies for other sites.
- **Preview while downloading.** Watch or listen to the part that is already downloaded, browse archives, and switch a torrent to sequential download.
- **Notifications and integration.** Telegram, LINE, Discord, Slack, ntfy, Gotify, Bark, QTS notifications and signed webhooks; a REST API with personal access tokens and an event stream ([docs/INTEGRATION.md](docs/INTEGRATION.md)).
- **AI agents.** Claude Code, Codex, Gemini CLI and other agents that read skill files can add and manage downloads for you, with a token of their own and a skill file served by your NAS ([docs/AI-AGENT.md](docs/AI-AGENT.md)).
- **Works with existing clients.** Qget, Qfile and browser extensions keep working through a compatible `/downloadstation/V4/` endpoint once Download Station is removed or disabled.
- **Import from Download Station.** Settings, unfinished tasks, history and site accounts. The import only reads the official data and can be run again.
- **Runs anywhere you open it.** Inside the QTS desktop, in its own browser tab, or on a phone; light and dark themes; 13 languages.

## Requirements

- A QNAP NAS with QTS on x86_64, ARM64 or ARMv7 (the `arm-x41` and `arm-x31` package architectures).
- The QTS administration web server (ports 8080/443), which serves the interface at `/DownloadCenter/`.

## Install

1. Download the `.qpkg` for your NAS from the Releases page.
2. In App Center, choose **Install manually** and select the file.
3. Open Download Center from the QTS desktop or at `https://<your NAS>/DownloadCenter/`.

QTS administrators can use Download Center right away and are its administrators. Allow other accounts in QTS: **Control Panel → Privilege → Users → Edit Application Privilege → Download Center**. **Settings → Users** lists who has access. On first start Download Center grants the accounts that could use the official Download Station.

## Build

The control daemon `dcd` is Go; the two engines `dc-bt` (libtorrent) and `dc-dl` (libcurl) are C++ built statically with Docker.

```sh
sh tools/build-dcbt.sh [arch]     # libtorrent engine, cached in tools/cache/
sh tools/build-dcdl.sh [arch]     # URL engine, reuses the toolchains and OpenSSL built above
GO=/path/to/go sh build.sh [arch] # vet, build dcd, lint, package with qbuild into build/
(cd daemon && go test ./...)      # unit tests
```

`qbuild` comes with QNAP's QDK and runs on the NAS. Releases are built on the NAS too: `sh tools/release.sh <version>` checks, builds, signs `SHA256SUMS` with the release key and uploads a draft release; pushing its tag lets the release workflow verify the files against `tools/release-key.pub` and publish them. Every release lists its changes in [CHANGELOG.md](CHANGELOG.md). The engine build scripts download every third-party source archive from its upstream site and check it against a pinned SHA-256 hash.

## Repository layout

| Path | Contents |
|---|---|
| `daemon/` | Go module: `cmd/dcd` (the daemon), `internal/` (core, engines, REST API, V4 compatibility, importer, notifications) |
| `dcbt/`, `dcdl/` | Engine sources and their protocol with `dcd` |
| `shared/` | Service script and the web interface (ES5, no build step) |
| `tools/` | Engine build scripts |
| `docs/` | Integration API, AI agent guide and skill, interface design notes and a clickable prototype |

## Use it lawfully

Download Center downloads whatever links, torrent files or magnet links you give it. It does not provide, search for or recommend any content.

- Only download content you have the right to obtain. Downloading or sharing copyrighted software, video, music or other works without permission may be illegal where you live.
- BitTorrent uploads the pieces you have to other people while you download, and they can see your IP address.
- You are responsible for what you download and how you use this software. As stated in the [license](LICENSE), it comes without warranty, and the authors are not liable for any damage or legal consequence arising from its use.
- **Client identity** (Settings → Download → Torrents) can make the torrent engine report itself as another BitTorrent client. Some private trackers forbid this and may ban accounts that do it. Leave it at "Download Center" unless you know your tracker's rules.
- When you use a file-hosting account, the hosting service's terms of use apply.

The interface shows a short version of this notice the first time each account signs in.

## Trademarks

QNAP, QTS, Download Station, Qget, Qfile, File Station and App Center are trademarks of QNAP Systems, Inc. Other product and service names (such as uTorrent, Transmission, Deluge, Telegram, LINE, Discord, Slack and the file-hosting services) belong to their owners. They are used here only to describe compatibility; no endorsement is implied.

## License

Download Center is released under the [MIT License](LICENSE).

The engines are statically linked with third-party libraries (libtorrent, Boost, OpenSSL, curl, nghttp2, libssh2, zlib, JSON for Modern C++, musl and the GCC runtime), and `dcd` with Go modules (modernc.org/sqlite and its dependencies). Their licenses and notices are in [THIRD-PARTY-NOTICES.txt](THIRD-PARTY-NOTICES.txt), which is also included in every package. The installer script that QNAP's QDK adds to a `.qpkg` is QNAP's own and is licensed under the GNU GPL.

## Support the project

Download Center has no ads and no paid version. If it is useful to you, you can [buy me a boba](https://tasict.bobaboba.me) (paid by card, no PayPal account needed) or [tip with PayPal](https://paypal.me/tasict).
