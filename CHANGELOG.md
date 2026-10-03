# Changelog

Each release has a section here; `tools/release.sh` refuses to release a version without one, and the section becomes the GitHub release notes. Versions follow semantic versioning; a version with a suffix such as `1.1.0-beta.1` is published as a pre-release.

## 1.0.1

- Who can use Download Center is now set in QTS: Control Panel › Privilege › Users › Edit Application Privilege. QTS administrators can always use it and are its administrators. Accounts on the old user list, and whoever could use Download Station, are allowed automatically when you upgrade.
- A new folder picker with a filter, free space, and a button to create folders. Read-only folders are marked and cannot be chosen; hidden and system folders are left out.
- Links on HTTP/2 servers now really download over up to four connections. Before, the extra connections shared the first one, which is much slower from distant servers.
- HTTPS downloads prefer ChaCha20 on NAS models whose CPU has no AES instructions, such as the 32-bit ARM models; it is much lighter on those CPUs.
- Torrent settings follow the NAS's cores and memory, and TCP peers are no longer held back in favour of uTP peers.
- The about page links to the project site, the releases and the issue tracker.
- The update page only offers newer versions; going back to an older one is no longer offered.

## 1.0.0

- Torrents now download into the hidden `@DownloadCenterTemp` folder like links, and move to their destination as soon as their data is complete; they keep seeding from there. Half-downloaded files never show up in your shared folders, and a move to another volume is copied out of sight first.
- A name that is already taken at the destination gets " (1)" instead of replacing anything.
- Torrents imported from Download Station leave its temporary folder once complete.
- Removing an unfinished torrent also removes its partial data, as it already did for links.
- Live traffic graphs in the connections tab of the task details.
- Optional anonymous usage statistics: once a day, counts only (never names, links, accounts or paths). The administrator decides in the usage notice.

## 0.9.1

First public release.

- Downloads HTTP and HTTPS (HTTP/2, up to four connections per file), FTP, FTPS, SFTP and SCP links, `.torrent` files and magnet links, with resume after restarts. Thunder, FlashGet and QQ links are decoded.
- One list for links and torrents: paste links, text or a web page address, or drop `.torrent` files; magnet links show their files before you start.
- Per-type concurrency and speed limits, a weekly schedule, timed pause, seeding by ratio or time.
- A temporary location and an optional folder to move finished downloads to; regular users always download into their own home folder and own their files.
- Users and roles with your NAS account; administrators see everything, regular users only their own downloads.
- HTTP and SOCKS5 proxies for URL downloads and torrents, per task or per site.
- File-hosting accounts: 1fichier, Rapidgator, Real-Debrid and AllDebrid through their official APIs, or cookies for other sites.
- Preview while downloading; sequential download for torrents.
- Notifications to Telegram, LINE, Discord, Slack, ntfy, Gotify, Bark, the QTS notification center and signed webhooks; downloads can be controlled from Telegram and LINE chats. REST API with personal access tokens and an event stream.
- Qget, Qfile and browser extensions keep working through a Download Station V4-compatible endpoint.
- Import of settings, unfinished tasks, history and site accounts from Download Station (read-only).
- Web interface for the QTS desktop, a browser tab and phones; light and dark themes; 13 languages.
