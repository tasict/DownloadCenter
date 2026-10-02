# Changelog

Each release has a section here; `tools/release.sh` refuses to release a version without one, and the section becomes the GitHub release notes. Versions follow semantic versioning; a version with a suffix such as `1.1.0-beta.1` is published as a pre-release.

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
