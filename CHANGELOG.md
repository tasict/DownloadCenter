# Changelog

Each release has a section here; `tools/release.sh` refuses to release a version without one, and the section becomes the GitHub release notes. Versions follow semantic versioning; a version with a suffix such as `1.1.0-beta.1` is published as a pre-release.

## 1.1.2

- When a video you preview has a subtitle file with the same name in the same folder (.srt or .vtt), a Subtitles switch below the player shows them. Your choice is remembered, and subtitles that are not in UTF-8 can be read with another text encoding.
- In queue order, every task can be dragged to a new place, including finished, seeding and failed ones. Finished and seeding tasks do not take a download slot, so moving them changes only where they appear in the list; a failed task queues from its new place when you retry it.
- On phones, the AI Agent section in Settings › Access tokens and the token windows no longer cover their own text, and addresses are no longer broken in the middle of a word.
- For the API: .vtt files are previewed as text.

## 1.1.1

- Updates download several parts of the package at once. Where each connection to GitHub is slow, this is many times faster; the package is still checked against the signed release list.
- You can close the page while Download Center updates. Opened again, it shows the progress, and an update that stopped before installing says why, here and in Settings › About and updates.
- The handle for changing the download order no longer shows when the list is sorted by something other than queue order.
- Open on phone moved into the sign-in card, beside the appearance switch. In Personal settings it is now one of the actions above Sign out.

## 1.1.0

- Drag tasks to change the download order. In queue order, drag a task by the handle at the end of its row, or by the row itself with a mouse. Before you let go, the gap says what dropping there does, for example that the task starts now and which one waits instead; afterwards the message says what really started or went back to waiting, with Undo. Several selected tasks move together. On phones, choose Select and drag the handles. With the keyboard, press Space on the handle and use the arrow keys, or Alt+↑/↓ on a row.
- Waiting tasks show their place in the queue, such as “Queued: #1 in the torrent queue”.
- Open on phone: on a computer, the sign-in page and Personal settings show a QR code of the page, so you can carry on with your phone. The code holds only the address; you sign in on the phone.
- Torrents now introduce themselves as what they are by default: libtorrent, with the peer ID `-LT20F0-` and the User-Agent `libtorrent/2.0.15.0`, which agree with each other. Settings › Download › Client identity shows what is sent. Changing the client identity restarts the torrent engine, so every torrent announces with the new one.
- Private torrents are never merged. Adding the same private torrent again, for example with another passkey, leaves the task as it is instead of adding trackers, and a private torrent with the same files as a task in the list is added as a task of its own. The add window and the task details say when a torrent is private.
- Site accounts that use cookies send them again; the cookies were dropped before the download started.
- Turning peer exchange on or off now takes effect.
- A new share ratio or seeding time also applies to torrents that are already seeding.
- Links to .torrent files sent from Qget, Qfile, browser extensions or chat commands are added as torrents. Chat commands also understand thunder://, flashget:// and qqdl:// links.
- A notification channel with no events chosen sends nothing, as its settings show.
- A webhook secret made by Download Center is shown once so you can copy it.
- Schedule switches are notified, and a low disk space notice comes once until space is back.
- The public address used in notification links and by LINE can be set in Settings › Notifications & integrations. Enter the address of the NAS; Download Center adds its own path.
- Chat replies show readable error messages.
- The preview starts with the largest audio or video file of a torrent.
- Several settings texts now say exactly what happens: torrent proxies, deleting a default proxy, limited-speed periods, the import and the usage statistics.
- For the API: `PATCH /tasks/{id}` takes a position next to another task (`{"before": id}` or `{"after": id}`) and `POST /tasks/bulk` the action `move`; both answer which tasks started or went back to waiting (`started`, `stopped`). Waiting tasks have `queue_rank`, `GET /tasks/{id}` has `private` for private torrents, `GET /settings` has `bt_identity`. New error codes: `task_moving`, `private_torrent`; unknown bulk actions are refused.

## 1.0.3

- AI agents can manage your downloads. Settings › Access tokens has an AI Agent section: create a token for the agent and run the command it shows on the computer where Claude Code, Codex, Gemini CLI or another agent that reads skill files runs. The command installs a skill file served by your NAS; the agent can then add, check, pause and remove downloads for you. Its token never does more than your account and cannot delete files unless you allow it.
- The first time an administrator opens Download Center on a NAS with Download Station, it offers to import Download Station's settings, tasks and site accounts. The import stays available in Settings › Import from official version.
- A new default folder set in the settings is offered in the add window right away. The add window only remembers the folder of your last task when you picked one other than the default.
- Links that cannot be added say why, for example that this NAS cannot download that kind of link, instead of showing an internal code.
- Magnet links are shown in purple in the add window; red is kept for input that cannot be added.
- Reloading the page keeps you on the page you were on.
- Magnet tasks take the name of their torrent as soon as its file list has arrived.
- Torrents no longer leave a hidden part file in the download folder once they are removed or finished; the ones left by earlier versions are cleaned up.
- The add window shows the folder a torrent is saved as.
- The interface is now written in English and translated into the other languages, Traditional Chinese included; a few English texts read slightly differently. For the API: a request without `X-DC-Lang` is answered in English, and adding a link that cannot be downloaded answers with its own error code (`url_unavailable`, `bt_unavailable`, `folder_read_only` …).

## 1.0.2

- The traffic graph in the connections tab of the task details shows again. It was cut down to a thin strip in which only half of the download and upload labels could be seen.
- The about page shows the notes of the installed version and of the latest one, instead of every release. Older releases are on the releases page.

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
