# Changelog

Each release has a section here; `tools/release.sh` refuses to release a version without one, and the section becomes the GitHub release notes. Versions follow semantic versioning; a version with a suffix such as `1.1.0-beta.1` is published as a pre-release.

## 1.4.5

- Some servers send their own certificate without the intermediate certificate that links it to a trusted authority. Browsers fetch the missing certificate themselves, and now Download Center does too, so such downloads no longer stop with "the server does not send its complete certificate chain". The missing certificate is fetched over plain HTTP from the address the server's certificate names, only from public addresses, and is only used to complete the chain: the chain still has to end at an authority the NAS trusts.
- New downloads from links can skip the server's certificate check: the add window has a "Skip certificate check" switch, on by default, and the task details can change it for a task that has not finished. A task that stopped because the server's certificate could not be verified offers "Skip certificate check and retry". With the switch on, a download goes ahead even when the certificate is self-signed, expired or for another name, but nothing then protects its connection from being read or changed on the way, account details sent with it included. Tasks added before this version, and tasks added through the API, Qget and other clients, chat commands, AI agents, the Deskpane widget or the import, keep checking certificates unless the switch is turned off in their details.
- The REST API takes `skip_cert_check` when adding a task (`POST /api/v1/tasks`) and when changing one (`PATCH /api/v1/tasks/{id}`), and reports it for URL tasks; the `error` of a URL task stopped by a refused certificate has `"certificate": true`.
- New HTTPS connections start faster: the download engine now reads the list of certificate authorities once instead of for every connection.
- A usage report that cannot be sent is logged when it starts failing and then once a day, not every 30 minutes.
- The log line written when the torrent engine starts names the versions of dc-bt and libtorrent; problem reports no longer show it as an address.
- Updates no longer write "getcwd" warnings into the update log.

## 1.4.4

- When a download stops because the server's certificate is refused, the message now names that server, which after a redirect may not be the host in the link, and says why: the server does not send its complete certificate chain, the certificate is self-signed or comes from an authority the NAS does not trust, it has expired or is not valid yet (check the NAS's date and time), or it was issued for another name. Until now every such download said that the NAS's list of certificate authorities might be out of date.

## 1.4.3

- New: Settings › About and updates › Problem reports. Administrators can download the logs of Download Center's background programs, mainly their errors, as one file to attach to a problem report. The file leaves out what was downloaded and who downloaded it: task names, file names, links, host names, addresses and account names are replaced, and tasks appear as diagnostic codes. A task's details show administrators its diagnostic code, and the same settings page finds the task behind a code.
- Logs no longer grow without limit: Download Center's own log and the logs of the two download engines keep 2 MB each plus one older copy, the update log 1 MB and the watchdog log 256 KB. A message repeated within a minute is written once with the number of repeats, and nothing is written while nothing happens.
- A task's log keeps its last 200 lines, and the same message within 10 minutes is not added again, so a task that keeps retrying no longer fills its log.
- Starting, stopping and the watchdog no longer run at the same time, and the watchdog writes nothing while Download Center runs normally.
- A failure while removing a finished task or restarting a download engine for new settings now ends only that piece of work instead of the whole program.

## 1.4.2

- When the service script lost track of a running Download Center, its watchdog started another copy every minute: URL downloads were interrupted, uploads in progress failed and the system log showed "dcd was not running; restarting" every minute. Only one copy now runs per installation, the service script finds the running one again, and stopping or updating the package stops it.
- The system log gets at most one "dcd was not running; restarting" warning an hour, with the number of restarts since the previous one and, when Download Center could not start, the reason. The details of each restart are kept in `data/logs/watchdog.log` in the package folder.
- When Download Center did not stop cleanly, the log of that run is kept as `data/logs/dcd.log.crash`. Starting Download Center while it runs no longer replaces the log of the running copy.
- A failure in the background housekeeping, in moving completed files, in resolving file-hosting links or in handing files to their owner now ends only that piece of work instead of the whole program.

## 1.4.1

- Download Center now does much less work for torrents that have not changed: it no longer asks the torrent engine for their status and file lists every few seconds, and it saves tasks to its database only when something about them changed, so seeding torrents no longer cause disk writes every few seconds.
- On NAS models with 1.5 GB of memory or less, the torrent engine uses smaller buffers and peer lists and checks one torrent at a time, and unless the settings were saved before, such models now default to 2 torrents downloading at once and 100 connections in total.
- New setting: Torrents seeding at once (Settings › Download › Torrent). Torrents beyond the limit wait to seed in list order and start when another one stops seeding or has not uploaded anything for 30 minutes; time spent waiting does not count as seeding time. There is no limit by default; new installations on NAS models with 1.5 GB of memory or less start with 3.

## 1.4.0

- Search for torrents by keyword: type what you are looking for in the new Search page, and Download Center asks every enabled search source at once and lists what they find as the answers arrive. Filter by source or category, sort by seeders, size, date or name, and add a result with one click through the usual add window. Typing words instead of a link in the add window searches for them too. Thanks to Sam for suggesting it.
- Search sources. Download Center comes with sources for freely distributable content: the Internet Archive (items their uploaders marked with an open license), Kiwix (Wikipedia and other reference sites for offline reading), Fedora, Arch Linux and, switched off until you turn it on, FOSS Torrents. Administrators can add more in Settings › Search: a Torznab server such as Jackett or Prowlarr on another machine, or a plugin file that describes a site's search API. Built-in sources, Torznab servers and plugin files are descriptions, not programs, and cannot run code on the NAS. The format is documented in docs/SEARCH-PLUGINS.md.
- Search plugins from another NAS: administrators can also install search plugins compatible with Synology Download Station (.dlm search modules) and QNAP Download Station (the search part of .addon add-ons). These are programs written by third parties. Download Center checks the file first, shows what it declares and installs it only after an administrator agrees to the terms for that exact file; it then runs each search in a restricted environment that keeps it away from your files and your local network, with QTS's built-in PHP (QTS's web server must be on), and lets it reach only public sites on ports 80 and 443. Plugins written for older PHP versions may not work, and a site that hands out .torrent files only to signed-in visitors works through its magnet links only. Download Center is not affiliated with Synology or QNAP.
- Each search source is for everyone or for administrators only. Torznab servers, sources with an API key and plugins from another NAS start for administrators only, since their links can identify your account on the site; built-in sources start for everyone.
- Searches are made by the NAS, through the proxy your downloads use, and never reach the NAS's own or loopback addresses, so a search source cannot run on the NAS itself. API keys and passwords of search sources are stored on the NAS and never shown again. Search words are not written to logs or kept after the results expire.
- Access tokens have a new permission, Search for torrents, which no preset includes: tokens you already have cannot search until you add it. The REST API can search and list search sources, and the agent skill explains how.
- HTTPS trackers, HTTPS downloads and update checks now also work on older QTS versions whose list of certificate authorities is out of date: Download Center brings an up-to-date list and uses it together with the system's. A tracker whose certificate cannot be verified now says so instead of only being unreachable.
- Torrents that were seeding from their folder before the update keep seeding as long as their files are unchanged, and adding a torrent whose files are already in the destination continues with them instead of downloading it again as “Name (1)”, as long as they match the torrent (a file that is only incomplete is completed). If any of the files differs, nothing of it is used: the torrent downloads as new, and one that was seeding stops seeding.
- Download Center is now available in Hungarian, translated by @dokukaninjarobot120. Thank you!
- Updating Download Center is easier to follow: the update window shows the steps (download, file check, backup, install and restart) with the one in progress animated, a ring with the download progress, and the time since the update started. When the package restarts, the window keeps moving until the new version answers, then shows a check mark and reloads the page.
- When Download Center restarts while its page is open, for example from App Center or after an update started in another browser, the page says it is reconnecting and picks up again by itself; after an update to a new version it reloads with the new version.
- A page that opens while Download Center is still starting waits for it and continues by itself, instead of showing an error.

### Changes you will notice

- Every download, also an administrator's, now runs in a private folder of Download Center at the top of the volume it ends up on, and appears in its folder only once it is complete; File Station no longer shows half-downloaded files. The free space in the add window is that of the volume the download ends up on.
- A finished torrent is handed over into its folder and keeps seeding from a private copy: deleting or replacing its files in the folder while it seeds does not change what is shared, and frees the space only when seeding ends or the task is removed.
- After the update, unfinished downloads are moved into the private folders once and torrents are checked again.
- AI agents in your browser ask you more often before they change anything (see Security).
- The usage notice was revised for search and is shown once more to every account.
- Accounts that are not administrators can have up to 10 notification channels, and a torrent file can be up to 16 MB (several uploaded at once up to 32 MB together).

### Security

- Downloads can no longer be used to reach other files on the NAS: they run in folders users cannot change, and Download Center no longer follows links that someone placed in download, home or temporary folders when it saves, moves, previews, hands over, seeds or deletes files.
- Downloads added with an access token, from a chat or by an AI agent in your browser never connect to the NAS's own addresses, also for administrators; redirects, web seeds and trackers are checked too, and the address is checked again before every request, also on a connection that is reused.
- AI agents in your browser (WebMCP) can no longer make a series of changes on their own: after the first small change in a page, every change they ask for is shown to you first, and so is any change that picks folders, adds trackers of their own or points at the NAS or your local network. The question lists everything and starts on Cancel.
- The setup command for AI agents saves the address and token so that nothing in them can run, and it is only offered for an ordinary NAS address; over plain HTTP the page warns that the token travels unencrypted.
- Signing out also ends this browser's “don't verify again on this device” for 2-step verification and forgets the recent searches kept in the browser. The “Use encrypted connection” button only ever leads to this NAS.
- The web server settings Download Center installs are readable by the system only and carry a secret, so other programs on the NAS can no longer pass themselves off as a visitor's browser.
- Uploads, magnet link look-ups and requests of Download Station clients are limited in size and number, so they cannot exhaust the NAS's memory; Download Station clients are refused for a while after repeated failed sign-ins, and another website can no longer sign you out through them. A server that reports an impossible file size stops only that download.
- Passwords inside download links are no longer shown in the task details or the API. An open event stream ends when its account loses the rights to it.
- Task names can no longer change how Slack and Discord notifications look, delivery errors keep only the site of an address, and LINE messages cannot be replayed.
- Updates installed from the app check the version and file names more strictly, and the release workflow only publishes tags of the main branch and checks packages with the key kept there. The download links of the website only lead to this project's release files.
- A search plugin added from an address must be the file you checked. Replacing a search source keeps its settings and API keys only when the new one is of the same type, asks the same full request address, builds its links the same way and has the same settings. A result's .torrent is only fetched from public addresses, two at a time per account, and magnet links of results drop trackers on local addresses. Search plugins from another NAS run with a memory limit and a system-call filter, and do not run where the NAS cannot apply them.
- Malformed or very large subtitle files can no longer freeze the preview.

## 1.3.0

- Magnet links that stayed at “Getting file list” for hours, or torrents that stopped finding anyone, no longer need the package to be restarted. A torrent that has had no connection and no progress for 15 minutes forgets the peers it gave up on and asks DHT and its trackers again, then after 30 and 60 minutes and every 2 hours, until it connects. After Download Center starts, torrents also ask DHT again as soon as it is up, instead of up to 15 minutes later.
- When DHT cannot start, for example because the network was not ready when the NAS booted, it now tries again by itself.
- A torrent that has found no peers for 15 minutes no longer holds one of the places in Settings › Downloads › Concurrent downloads, so the next torrent in the queue can start. It keeps looking and takes its place again as soon as it connects.
- A magnet link getting its file list shows how long it has been waiting.
- The task details of a torrent or magnet link that finds no peers now start with its source status: whether it is still being looked for, nobody seems to be sharing it, or it has probably stopped being shared. A gauge shows how long it has waited and when that judgment changes, and the details list what it is based on: whether it ever connected, what the search for peers found, what each tracker reported and when anyone last had the whole download. The NAS itself, which the network hands back to it, no longer counts as a peer that was found. Look for peers again right away, add the public tracker list or add trackers from there; failed torrents offer the same.
- Tasks that nobody seems to be sharing, or that have probably stopped being shared, are marked in the task list.
- The Connections tab lists every tracker of a torrent with its state and the number of complete copies and downloaders it reports.
- Settings › Downloads has a new section for torrents that find no peers: it holds the public tracker list settings and sets when a torrent counts as not shared (6 hours by default) and as probably no longer shared (3 days by default). Only the time a task runs counts. Download Center never deletes such a task by itself.
- Notification channels and webhooks can be told when a task has probably stopped being shared.
- Download Center keeps a list of public trackers, updated from its website, that you can add to a torrent, or to several selected torrents at once. Settings › Downloads can also add it to every new torrent; this is off by default. Private torrents never get other trackers: a magnet link that turns out to be a private torrent drops the trackers added to it.
- Torrents added by users who are not administrators, and every torrent that gets the public tracker list, no longer contact trackers, web seeds or peers at the NAS's own or loopback addresses, whatever the magnet link or torrent file lists, also when torrents use a proxy. A task takes up to 200 trackers besides its own and the public list.
- The add window no longer shows the file list of a torrent that another user is downloading; it says that someone else is downloading it, as adding it would.
- Trackers added to a torrent are announced to right away, together with the others, instead of only after the others failed.
- AI agents using WebMCP can add trackers or the public tracker list to torrents, and see whether a torrent has found any peers and whether anybody seems to share it; they are told to leave deleting to you. The REST API has the same, and the agent skill explains when to use it.

## 1.2.2

- When a torrent or magnet link has several files to download, the largest video among them gets high priority, so the movie itself finishes first and can be previewed sooner. The other files are set to medium, and the task log names the video that was chosen; you can change any of them in the task's Files tab.
- File priorities set for a torrent now always reach the download: they are kept when Download Center restarts, and setting a file back to a plain download, also from Qget, clears its earlier priority.

## 1.2.1

- On computers, Select next to the sort menu turns on selection mode, as on phones: every task shows its checkbox, clicking a task checks it, and the bar of batch actions starts with Select all, which checks every task in the current filter. Shift-click checks or unchecks every task between the one you clicked last and this one, Ctrl-click (⌘-click on a Mac) adds a task to the selection, and Esc leaves selection mode.
- The Completed list has Clear completed, which removes all completed tasks from the list after you confirm. Their files are kept unless you choose to delete them too, and you can undo it.
- Tasks can be sorted by the date they were added or the date they finished, newest first. Sorted by date finished, tasks that are still downloading come last.
- When the sort menu and its buttons do not fit beside the list title, for example in German on a phone, they continue on the next line instead of running off the screen.

## 1.2.0

- Download Center brings two widgets to Deskpane's desktop and phone home screen. Download queue shows how fast it is downloading, what is in progress and anything that needs a look, such as a failed download, low disk space or the schedule pausing downloads; the large size adds the download speed of the last 15 minutes, and a click on a task opens it in Download Center. Add download takes URLs, magnet links and .torrent files dropped on it, or a URL pasted into it, and adds them with your usual settings; on a phone, Paste adds what is on the clipboard.
- AI agents in your browser can use Download Center directly through WebMCP, without a token. With Download Center open over HTTPS in a browser that offers WebMCP (Chrome with chrome://flags/#enable-webmcp-testing turned on, or an extension that supports WebMCP), the agent can see how your downloads are doing, add links, pause, resume, retry and reorder downloads, choose the files of a torrent and show a task in the page. It acts as you, with what your account may do. Deleting tasks always asks you in the page first, and the page tells you about every change the agent makes. Settings › Access tokens › AI Agent says whether your browser offers WebMCP and has a switch to turn this off.
- A link to a task's details, such as one opened from a widget, opens the task in Download Center, also when Download Center is already open.
- Tasks added from a Deskpane widget show Deskpane as where they came from, next to the owner in the task details.
- While you work in Download Center, an open QTS desktop tab in the same browser no longer signs you out when its idle time runs out, and neither does the QTS desktop around Download Center's window. Your idle logout setting in QTS still applies when you stop working.
- A busy QTS no longer signs you out: only QTS saying that the sign-in has ended does.
- The daily usage statistics also count how the widgets are used: which widget at which size, and how many downloads were added from them.

## 1.1.3

- Google Drive links to files shared with “Anyone with the link” download the file itself, without an account. Before, such a task finished with Google's preview page or virus-scan warning saved in place of the file. The file's name and size are known before the task is added, and the add window labels these links Google Drive. When Google hands out no file, adding says why: the file does not exist, is not shared publicly, or has been downloaded too often for now. Folder links are not supported yet; open the folder and add the links of its files.
- A web page that Google Drive sends in the middle of a download, for example once a file has been downloaded too often, stops the task with an error instead of replacing the data downloaded so far.
- A cookies account you saved for Google is used for Google Drive links too, unless the site account is set to None.
- The daily usage statistics also count how access tokens and the API are used: the preset, scopes, expiry and limits of new tokens, which API routes, V4 endpoints and chat commands are called, the kind of client, and error codes. Token names, owners and values, task ids and links are never sent, and the web interface's own requests are not counted.
- In the usage notice, administrators find the statistics switch at the end, after what is and is not sent.
- For the API: Google Drive tasks have `hoster` `gdrive`. `GET /me` lists Google Drive in `hosters` with `no_account`; `GET /accounts` does not offer services that need no account.

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
