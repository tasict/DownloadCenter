# Download Center UX/UI design

Clickable prototype: `docs/prototype/index.html` (sample data, no backend, ES5, portable to `shared/web/`). What the prototype covers is listed at the end.

UI copy is written in English, which is the translation source (Traditional Chinese and the other languages are dictionaries); this document quotes the English UI strings.

## Audience and main tasks

- **Audience**: NAS users at home or in a small office, including regular users, not only administrators; often on a phone. Most do not know BitTorrent terms and do not want to pick "what kind of download this is" first.
- **Main tasks** (by frequency):
  1. Paste one or a batch of URLs or magnet links, or drop a `.torrent` in; or type a few words and search the torrent sources set up on the NAS.
  2. Take a glance: is it still running, how fast, how long to go.
  3. Find the files once they are done.
  4. Pause, resume, delete, occasionally reorder.
  5. Choose which files of a torrent to get, where they are kept while downloading and where they go when finished.
  6. Occasionally change settings: speed limits, schedule, site accounts, tokens, notifications.
  7. Once: import from the official Download Station.

## Pain points of the official package

| Official package | This design |
|---|---|
| Opens only after signing in to the QTS desktop; barely usable on a phone | **Own sign-in page**, reachable by typing the address; one responsive UI for phones and desktops |
| Adding a task starts by choosing between separate URL / BT / magnet dialogs | **One input box** that takes anything and works out the type; `.torrent` files can be dropped anywhere on the page |
| You find out you have to pick files only after the torrent was added | After "Add", **one dialog** settles everything: which files, where to keep them while downloading, where to move them when finished, which site account to use |
| Dense ExtJS grids | One row per task, hairline separators; everything else lives in the details panel |
| The schedule is 168 checkboxes | A 7×24 grid you paint by dragging, with a plain-language summary written underneath as you go |
| Nothing for other programs | Access tokens, REST API, webhooks, chat bots (see `docs/INTEGRATION.md`) |

## Design direction: Liquid Glass (after macOS / iOS 27)

Replaced the earlier minimalist look on 2026-10-02. It follows Apple's revised Liquid Glass in iOS 27 / macOS 27: glass is used only for the control layer floating above the content, with stronger background diffusion, clear layering and readable text; the content itself stays in solid grouped lists.

Three things on screen are memorable; everything else stays plain and system-native:

1. **Floating glass layers**: the sidebar, toolbar, search field, bulk-action bar, dialogs, details panel and toasts are translucent glass (30px blur, 180% saturation, a thin highlight on the top edge, soft shadow), kept apart from each other and from the window edges so they float over the content.
2. **BT piece progress bar**: a torrent's progress bar is 48 rounded pieces that light up out of order; a URL task has one continuous rounded bar.
3. **Animated symbols**: stroke weight follows SF Symbols (1.7); motion only shows state or answers an action, matching Apple's symbol animation types (bounce, pulse, rotate, wiggle, draw).

### Color (system colors)

| Token | Light | Dark | Use |
|---|---|---|---|
| `--bg` | `#F2F2F7` | `#000000` | Grouped background; a very faint accent glow at the top gives the glass something to refract |
| `--surface` | `#FFFFFF` | `#1C1C1E` | Grouped lists, cards |
| `--label` / `--label2` / `--label3` | `#1D1D1F` / `#6E6E73` / `#AEAEB2` | `#F5F5F7` / `#98989F` / `#636366` | Primary, secondary and hint text |
| `--fill` / `--fill2` | gray 12% / 20% | gray 24% / 36% | Secondary buttons, inputs, progress bar tracks |
| `--accent` | `#007AFF` | `#0A84FF` | Primary actions, downloading, selection |

State colors: waiting `#8E8E93`, paused orange `#FF9500` / `#FF9F0A`, seeding teal `#00A6B4` / `#40C8E0`, finished green `#34C759` / `#30D158`, checking and moving purple `#AF52DE` / `#BF5AF2`, error red `#FF3B30` / `#FF453A`.

Glass: `rgba(255,255,255,α)` / `rgba(30,30,32,α)`. α is set by the Appearance › Glass slider (0.30 to 0.95), after the transparency slider iOS 27 added. With the system's Reduce Transparency on, or a browser without backdrop blur, α is fixed at 0.97 and the glass becomes a solid material.

### Type

- System fonts: `-apple-system, "SF Pro Text"`, Chinese `"PingFang TC"`, falling back to `"Noto Sans TC"` and `"Microsoft JhengHei"` on other platforms. The NAS may be offline, so no web fonts are loaded.
- Large title (list title) 28px bold, letter spacing -0.02em; section titles 15px semibold; body 14px.
- Numbers such as speeds and verification codes use the rounded face `ui-rounded`; all numbers are `tabular-nums`.
- URLs, paths and tokens use `ui-monospace, "SF Mono"`.

### Component vocabulary

- **Sidebar**: floating glass panel, 22px radius; the selected item has a gray capsule background, icons use state colors. Search sits between the filters and Settings, under the separator. In narrow windows it becomes a horizontally scrolling capsule bar at the top; on phones it leaves out Search and Settings (they are in the bottom bar) and hides filters with no tasks.
- **Bottom bar** (phones only): a glass capsule tab bar "Tasks / Search / Settings" (Search only for accounts that have the search page) with a round accent "+" add button beside it; on the search page it folds into one round button and the search field; in selection mode it turns into a labelled bulk-action bar.
- **Toolbar**: floating glass capsule holding speeds, schedule and the account avatar.
- **Lists**: inset groups (solid, 16px radius), separators start right of the icon; state icons sit in a tinted circle; multiple selection uses round checkmarks.
- **Buttons**: capsules; primary actions are solid accent, secondary actions gray fill.
- **Switches**: every on/off is an iOS switch (green when on); only selection uses round checkmarks.
- **Segmented controls**: details panel tabs, settings tabs, Light / Dark / Auto in Appearance.
- **Settings**: like the iOS Settings app, each section has a colored rounded-square icon (blue, orange, green, purple, teal, red, gray in turn); content is grouped lists with labels on the left and controls on the right.
- **Dialogs**: three parts: title, scrolling content, buttons pinned to the bottom. On desktops centered, 28px radius, scaling up from 94% while fading in; on phones a floating sheet rising from the bottom, 6px from the sides and bottom, with a grabber at the top, closed by pulling down, moving up with the keyboard. While a dialog still holds unsent input (such as pasted URLs), clicking outside, `Esc` or pulling down only nudges it with a hint; only "Cancel" closes it. When the primary button is dangerous, such as delete, focus starts on "Cancel" so Enter cannot delete by mistake.
- **Details panel**: a floating inspector on the right, 10px from the edges, 26px radius; the first footer button is the main action (pause, resume, retry). In windows 1320px wide or more it docks beside the list (no dimming, the list stays clickable, clicking another row swaps the content); in narrower ones it overlays the list. On phones it is a nearly full-screen sheet rising from the bottom, closed by pulling down.
- **Toasts**: glass capsules at the bottom with a springy entrance; on phones they float above the bottom bar.
- **Sign-in page**: a background with soft color blobs (only on this page), a glass card in the middle, a 64px app icon above it.

## Layout

### Sign-in page

The flow follows what QTS `authLogin.cgi` actually answers. It covers only what QTS web sign-in uses: user name and password, 2-step verification (authenticator, backup email, security question), expired passwords, required 2-step enrolment. The package's own page does not do QNAP Authenticator scan/approve sign-in, Azure SSO (left to "Sign in with the QTS page" when redirect sign-in works), a language menu or "stay signed in" (`remme` / `qtoken`).

**The package backend never handles credentials; the session is QTS's sign-in cookie**: the page's JS signs in directly against QTS `/cgi-bin/authLogin.cgi` on the same origin (as QTS's own sign-in page does) and on success sets the `NAS_SID`, `NAS_USER` and `NAS_PW_STATUS` cookies the way QTS does. Lockout after failures, 2-step verification and session lifetime are all handled by QTS, and lockout is recorded against the browser's IP. A browser has only one NAS session: after signing in to the QTS web UI, Download Center opens straight away; after signing in to Download Center, the QTS web UI is signed in too.

- With a valid `NAS_SID` when the page opens, the sign-in page is skipped. An expired password is reported by QTS while signing in, which then shows the "password expired" screen.
- "Sign out" in the account menu also signs QTS out in this browser, and says so next to the button. Signing out also ends this browser's "don't verify again on this device" for 2-step verification (the confirmation says so) and forgets the recent searches kept in the browser. The sign-in page sets the QTS cookies exactly as the QTS sign-in page does (not Secure), so QTS over plain http in the same browser keeps working.
- The account menu is a popover under the avatar on desktops (no dimming) and a bottom sheet on phones; an appearance choice applies and is remembered immediately, without "Save". The toolbar avatar, like the one in the menu, is the QTS profile picture, or the first letter of the account name when there is none.
- **Terms of use**: shown once per account at first sign-in (cannot be closed by clicking outside or `Esc`, only with "I understand and agree"). It says the software only downloads links the user provides and offers or recommends no content; that search asks third-party sites the package does not run, that the built-in sources only use freely distributable content, that an Internet Archive item's license is its uploader's statement, and that sources an administrator adds (including search plugins from another NAS, which are third-party programs) are that administrator's responsibility; to download only what one has the right to; that BitTorrent shares data and exposes the IP address; who is responsible, that there is no warranty and the disclaimer; and that it has nothing to do with QNAP or Synology. Administrators make the usage-statistics choice at its end, with the switch showing the current setting (on when nobody has chosen yet); agreeing saves it only when no choice was made before or the switch was changed. The consent is stored in the user's settings as the notice's version; a revised notice (version 3 added search) is shown once more to every account; the text can be read again from "Terms of use" in the settings footer. The footer also has "Licenses" (the third-party license notices shipped in the package).
- **Updates** (administrators only): when a new version exists, a small blue tag "New version x.y.z" appears in the toolbar (icon only on phones); nothing pops up. Opening it shows the release notes as plain text with "Skip this version", "Later" and "Update now". During an update, the progress window keeps moving: a ring around the step's symbol (the downloaded share while downloading, a turning arc otherwise), the four steps Download › Verify files › Back up › Install and restart (done ones checked, the one in progress pulsing, a failed one red) and the time since the update started (`m:ss`, measured by the NAS and counted on while the package restarts). When the new version answers, the ring turns green and draws a check, and the page reloads by itself and reports the result once; a failure turns it red and shakes. With reduced motion everything stays still. When the package restarts while the page is open (App Center, an update from another browser), two failed list reads in a row show a glass capsule at the top, "Download Center is restarting. Reconnecting…" or "Cannot reach the NAS. Reconnecting…", with a turning circle; it says "Reconnected" when the list answers again, or reloads the page when the package came back as another version. A page opened while the package starts shows "Download Center is starting…" and continues by itself (every 3 seconds, up to 5 minutes); the plain "Starting…" fades in only when loading takes a moment. The update runs on the NAS, so the page may be closed: opened again meanwhile, it shows the progress window again (the toolbar tag stays hidden), and an update that stopped before installing says once why (Settings › About and updates says it too, and the update can start again). The package downloads in parts over up to six connections when the server takes ranges, because a slow route to GitHub's file servers limits each connection, not the line. Settings › "About and updates" shows the notes of the running version and of the latest release only (older ones are on the releases page); only a newer one has an update button. The UI offers no way back to an older version, so users do not hop between versions and mix up their data.
- **Support links** live only where users go on their own: the bottom of the account menu and the bottom of the settings page, each a single muted footer line (version, "Buy me a boba" with a small boba icon, "Support with PayPal"). No reminders, no badges, nothing in the task list or the add flow. Links open in a new tab; the boba icon ships with the package (`img/boba.png`) rather than being loaded from outside.

```
            [icon] Download Center
            Sign in with your NAS account
 ┌───────────────────────────────────────────────────────────┐
 │ This connection is not encrypted; the password is sent    │   only when opened over http://
 │ in plain text.                 Use encrypted connection   │
 └───────────────────────────────────────────────────────────┘
            User name  ________________
            Password   ______________ (eye)
            Remember user name             (switch)
            [          Sign in          ]
            Sign in with the QTS page          only if redirect sign-in works
            Same account as QTS. Opening from the QTS desktop signs you in directly.
            [ Light | Dark | Auto ]
```

**Screens and branches** (in the order QTS's answer is checked):

| QTS answer | Screen |
|---|---|
| `authPassed=1` | Then QTS's application privilege is checked: QTS administrators and accounts allowed to use Download Center go in; others stay on the sign-in page with "This account has no access to Download Center yet. Contact your administrator." |
| `user_pw_expiry=1` or `pw_status=1` | "The password of this account has expired. Change it before signing in." with a "Change password in QTS" button (opens the QTS sign-in page in a new tab). The package does not change passwords itself |
| `need_2_step_verification=1` | 2-step verification screen (below) |
| `force_2sv=1` | "Your administrator requires 2-step verification for this account, but it is not set up yet." with an "Open QTS" button |
| Anything else (wrong password, disabled or expired account, blocked by QTS) | In QTS's words, a single sentence "The login credentials are incorrect or the account is no longer valid." that does not reveal which one it was |

**2-step verification**:

- The default is the authenticator's 6-digit code: one field (`inputmode=numeric`, `autocomplete=one-time-code`, so phones can fill it from a text message or the authenticator) that submits by itself once 6 digits are in.
- A wrong code shows the NAS's current time: "The code is incorrect. Try again. The NAS time is 09:41; your authenticator's clock must match it." An out-of-sync authenticator clock is the most common cause.
- A countdown at the bottom left (3 minutes for the authenticator, 5 for backup email and security question, as in QTS); when it runs out, the page returns to the first step with "The verification time has passed. Sign in again."
- "Verify another way" lists the methods set up for this account: authenticator, send a code to the backup email (shown masked), answer the security question. After the email is sent, its code goes into the same field; too many wrong security answers disable the method and ask the user to contact the administrator.
- A "Don't verify again on this device" switch maps to QTS's `dont_verify_2sv_again`; the vtoken QTS issues is kept in this browser (localStorage) as the QTS sign-in page does, so the next sign-in on the same device skips the second step.
- Every screen has "Use another account" to go back to the first step.

**Other rules**:

- Opening `https://<NAS>:<port>/DownloadCenter/` lands on this page; opening from the QTS desktop reuses the QTS session and skips it.
- When opened over `http://` (except `localhost`), the page asks QTS for its HTTPS port and shows a warning line with "Use encrypted connection", which opens the same page on `https://` (the browser must share an origin with QTS's sign-in CGI to call it). When QTS only allows HTTPS, its own web server already sends `http://` requests there.
- While submitting, the button reads "Signing in…" and is disabled to prevent double submits.
- **Repeated failures** are QTS's business; the package keeps no count. QTS gives no reason when it blocks, so the screen shows the same failure sentence.
- "Remember user name" keeps only the user name in this browser, never the password. There is no "stay signed in"; session lifetime follows QTS.
- The password stays only in the sign-in page's memory, because the second step must send it to QTS again; it is cleared on success, timeout or leaving the page.
- "Sign in with the QTS page": appears only if QTS redirect sign-in (`redirect_uri`) works in testing, so QTS handles QNAP Authenticator, SSO and other methods the package does not.
- The eye button at the right of the password field shows or hides the password (passwords are easy to mistype on phones).
- The user name and password fields carry `autocomplete=username` / `current-password` so browsers and password managers can fill them.
- Light, Dark or Auto can be chosen at the bottom of the sign-in page too, before signing in.
- **Open on phone** (at the foot of the sign-in card, a key beside the appearance switch with the same height and fill; in Personal settings, the first of the actions above Sign out, styled like it): a window with a QR code of the page's address, the address itself with "Copy link", and a note that the code carries no sign-in, so the phone signs in on its own. The address leaves out the query (the QTS desktop adds its window id there) and, after `#`, keeps only the app's own view (`#tasks/…`, `#settings/notify`); on a LAN address (`localhost`, private IPv4, `.local`, IPv6 local) the window says it works only on the same network. The code is drawn in the page (no outside service), black on a white card in every theme, and develops onto the card once, corner to corner, when the window opens. Shown on computers only: touch devices (phones, tablets) do not get it; inside the QTS desktop it is always there, also in a window narrowed to the phone layout.

### Main screen (desktop)

```
Download Center                     ↓ 18.6 MB/s  ↑ 1.2 MB/s  ◷ Full speed, limited from 18:00  admin
──────────────────────────────────────────────────────────────────────────────────────────────────
All          9     All  9 tasks                  Sort [Queue order▾] [Select] [ + Add download ]
Downloading  3     ──────────────────────────────────────────────────────────────────
Waiting      1     ↓  ubuntu-24.04.3-desktop-amd64.iso
Paused       1        ▮▮▯▮▮▮▯▮▮▮▮▯▮▮▮▮▮▯▮▮▮  62%  3.66 GB / 5.90 GB  9.2 MB/s  4 min left
Seeding      1     ──────────────────────────────────────────────────────────────────
Finished     1     ↓  LibreOffice_25.8.1_Linux_x86-64_deb.tar.gz                    alice
Error        1        ─────────────────────                     41% …
───────────
Search
Settings
```

- There is no paste bar on the main screen. The accent **Add download** pill at the end of the list title opens the add window (see Add flow); on hover the three kinds it takes (a link, a magnet, a torrent file) fan out of it. `/`, pasting a link anywhere on the task list and dropping a `.torrent` on the page open the same window.

- Sidebar filters have state-colored icons and counts; the current one has a gray capsule background and bold text.
- **Sort**: a "Sort" menu at the right of the list title: queue order (default), state, progress, time left, time downloading, date added, date finished; the arrow beside it flips the direction, and each sort has a sensible default direction (state: what needs attention first, error → downloading → checking → moving → seeding → waiting → paused → finished; progress: high to low; time left: short to long, tasks without one last; time downloading: long to short; date added and date finished: newest first). Tasks that have not finished downloading stay last when sorted by date finished, whichever the direction; a seeding torrent counts as finished from the moment its download completed.
  - The values change every second, so a sorted list reorders at most every 10 seconds to keep rows from jumping under the pointer.
  - Sorting by progress, time downloading, date added or date finished shows that value on the rows ("Added Today 14:05", "Finished Yesterday 09:12"); time downloading counts only time actually spent downloading.
  - Move up and Move down work only in queue order; with other sorts the bulk bar hides those two buttons.
  - In queue order, every row can be dragged to a new place, finished, seeding and failed ones included: by the handle at the end of the row (shown on hover) or, with a mouse, by the row itself once it has moved 6 px, so a click still opens the details. The rows part around a slot whose note says what dropping there does, using only tasks of the same type that the user can see: "Drop to start downloading now; “Charlie” will wait" (another user’s task is never named), "Drop to wait; “Delta” starts downloading", "Drop to wait as #2 in the torrent queue", for a paused task that it stays paused and queues from there when resumed, for a failed one that it queues from there when retried, and for a finished, seeding or moving one that it goes there without taking a download slot. Dropping applies the order at once; the toast says what the server reports started or went back to waiting, with Undo. The list does not re-sort under the pointer while a task is in hand.
  - Several checked rows move together, in their queue order, with a count on the lifted row. Keyboard: Space or Enter on the handle picks the task up, arrows move it (Home/End to the ends), Space or Enter drops, Escape cancels; Alt+↑/↓ on a row moves it one place. The notes are read out by screen readers.
  - Waiting rows show their place: "Queued: #1 in the torrent queue".
  - The sort choice is a per-user preference, kept for the next visit.
- **Multiple selection**: "Select" beside the sort menu (as on phones) enters selection mode; so does checking a row's checkbox, which appears on hover, or Ctrl-clicking a row (⌘-click on a Mac, where Ctrl-click opens the context menu). In selection mode:
  - Every row shows its checkbox, and clicking a row checks or unchecks it instead of opening the details; the "Details" button at the end of the row still opens them.
  - The list title turns into the bulk-action bar: "Select all" (or "Select none" once everything is checked), the count, start, pause, move up, move down, delete, cancel. "Select all" checks only the tasks the current filter shows, so Completed › Select all › delete clears the completed tasks.
  - Shift-click on a row or a checkbox sets every task between the one clicked last and this one, in the order on screen, to what this click did (checked or unchecked).
  - Cancel, Escape (when no dialog or details drawer is open, otherwise Escape closes that first), a delete or another filter leave selection mode.
- **Clear completed**: in the Completed filter, for accounts that may delete tasks, a "Clear completed" button sits beside "Select". It takes every completed task shown through the usual delete dialog: files are kept unless "Also delete downloaded files" is checked, and a delete that keeps them can be undone.
- The main action at the end of a row (pause, resume, retry, open folder) is always shown; the "Details" button appears on hover. Row details (percentage, size, speed, time left) are laid out separately; more than 30 days left shows "More than 30 days left".
- Clicking a row (outside selection mode) opens the details drawer on the right: Overview (merged sources are listed here), Files (choose what to download), Preview, Connections (BT), Log.
- A magnet link getting its file list shows how long it has been waiting, in its row and in the Overview. A torrent that is not complete and has had no source for 10 minutes (or failed) starts its Overview with **Source status**, which answers "should I keep waiting?": a verdict with a signal icon (Still looking for peers, gray, the waves fade in turn; Nobody seems to be sharing it, orange; Probably no longer shared, red, the waves struck through, shaking once) and one sentence why; the **waiting gauge**, three rounded zones in the piece bar's vocabulary (still looking, not shared, probably dead) with the two thresholds under the borders and a dot for the time without a source, counted only while the task runs (a slow ripple around it while it runs, still when paused), plus what happens next ("If nothing turns up in 5 h 13 min, it will count as not shared"; paused: the time does not count; probably dead: Download Center never deletes it, use Delete below); then what it is based on, in plain words with a dot each (green: someone shares it, gray: found but not sharing, hollow: nothing or unknown, red: a tracker says the torrent is gone): Connection, Peer search, Trackers (opens Connections), Complete copies. When a tracker says the torrent does not exist there is no gauge. Under the card: "Add public tracker list (n)" as the primary button when the public list would add trackers, otherwise "Look for peers again" (rests for a minute afterwards and says so), and "Add tracker…" (one tracker per line, optionally with the public list); private torrents get no tracker buttons; no delete button here. Rows of torrents nobody seems to share, or that are probably dead, start their details line with an orange "Not shared" or red "Probably dead" capsule. The Connections tab lists what every tracker reports (state, complete copies and downloaders, which include this NAS), the public list folded into one row. Selection mode has "Add public tracker list" for several torrents at once, and Settings › Downloads › Torrents that find no peers has the switch that gives every new torrent the public list (off by default) with the list's state and "Update now", and the two thresholds.
- **Preview**: media gets a player, and its progress bar separates the contiguous part that can be played from what is downloaded but not contiguous; torrents can switch on "Watch while downloading" to download in order; archives list the directory as far as it can be read; disc images and other types that cannot be previewed say why.
  - **Subtitles**: when a video the browser can play has a `.srt` or `.vtt` file with the same name in the same folder of the task (`Ep01.mp4` and `Ep01.srt`; not `Subs/Ep01.srt` or `Ep01.en.srt`), a "Subtitles" switch sits under the progress bar, above "Watch while downloading", with the file name in small type. It is on by default; the choice is remembered for the account and follows the player's own captions menu. Until the subtitle file has finished downloading, or when it holds no readable cues, the switch is disabled and says why. Subtitles show in the UI's typeface, white on a translucent dark band, in both themes. Files that are not Unicode are decoded with a guess from the UI language (Big5, GB18030, Shift_JIS, EUC-KR, otherwise Windows-1252), and while the switch is on a "Text encoding" menu under it lets the user pick another one; the pick is remembered and tried first next time.

### Phones (≤ 560px)

Principle: one-handed use. The two most common things on a phone are "drop a link in" and "check progress", so the list is on screen as soon as the app opens and frequent buttons sit in the lower half within thumb reach; the top holds only information to read.

```
╭ ↓ 18.6  ↑ 1.2 MB/s  ◷ Full speed, limited fr…  (person) ╮   toolbar: one line, scrolls away
[All 9][Downloading 3][Paused 1][Error 1]…                    filter bar: sticks to the top; empty filters hidden
All  10 tasks                          [Queue order▾] [Select]
┌───────────────────────────────────────────────────────┐
│ ↓  ubuntu-24.04.3-desktop-amd64.iso              (⏸) │   one 44px main action at the end of the row
│    ▮▮▯▮▮▮▯▮▮▮▮▯▮▮▮▮  62%  9.2 MB/s  4 min left       │   tapping elsewhere on the row opens details
├───────────────────────────────────────────────────────┤
│ ✓  Sintel (Blender Open Movie)                   (📁) │
└───────────────────────────────────────────────────────┘

╭──────[ ↓ Tasks ][ ⚙ Settings ]──────╮   ( + )          bottom: glass capsule tab bar + round add button
```

- The **toolbar** shrinks to one line: speeds, schedule state (cut with "…" when too long; tapping it opens the schedule settings), account avatar. No brand name: the app name is already on the home screen icon and the tab title.
- The **filter bar** sticks to the top and scrolls sideways; filters without tasks are hidden (except the current one), so it usually fits on one line. Settings is not in the filter bar; it moves to the bottom tab bar.
- The **bottom bar** follows the iOS 27 Liquid Glass tab bar: a glass capsule with the Tasks, Search and Settings tabs on the left and a separate 60px round accent "+" on the right. Tapping Tasks again scrolls the list back to the top. Search is there only for accounts that have the search page (see Search).
- **Adding**: the main screen has no paste bar. "+" raises the "Add download" panel from the bottom with the input focused and the keyboard up, ready for a long-press paste; when the page is on HTTPS there is also a "Paste clipboard" button (browsers allow reading the clipboard only over secure connections). Below it is "Choose .torrent file". "Add" then switches to the same add dialog as on desktops. "Add" is disabled while the pasted text holds no link, unless it is one line of words while a search source can be searched: then it says "Search" (see Search).
- **Rows**: only one main action at the end (pause, resume, retry, open folder), as a 44px round button; the "Details" button is gone, tapping anywhere else on the row opens details. Downloading rows show only percentage, speed and time left; downloaded size is left to the details panel. Rows get a background on press.
- **Multiple selection**: "Select" next to the list title; a long press on a row also enters selection mode and checks that row. In selection mode:
  - Round checkmarks appear at the start of rows; tapping a row checks or unchecks it instead of opening details.
  - The title bar becomes "Select all / 3 selected / Done".
  - The bottom tab bar turns into the bulk-action bar: start, pause, move up, move down, delete, each with a label under its icon; disabled while nothing is checked.
  - In queue order each row shows a handle at its end (44 × 44px); pressing it starts the drag at once, so a long press on the row still only selects. A line under the title says so; with another sort it says to switch back to queue order.
  - "Done" or a delete leaves selection mode.
- **Clear completed** works as on wide screens, beside "Select" in the Completed filter.
- **Dialogs** rise from the bottom, 6px from the sides and bottom. Title and buttons stay put and only the middle scrolls, so however long the file list, "Start download" stays under the thumb; buttons are full width and 50px tall. Pulling the grabber or the title down closes it. The dialog moves up with the keyboard instead of being covered. In the add dialog, "Temporary location / Move to when finished / Site account / When finished" put the label above the menu.
- The **details panel** is also a nearly full-screen sheet rising from the bottom, closed by pulling down; the first footer button is the main action (pause, resume, retry, stop seeding), followed by open folder and delete.
- **Settings** follows the iOS Settings app: first a grouped list (colored icon, name, a one-line description, ›); tapping one opens that section, and "‹ Settings" at the top left goes back. No row of seven tabs scrolling sideways.
- The **schedule** grid turns on phones: weekdays across the top, the 24 hours running down, so seven days fit without sideways scrolling. A drag that starts on the grid paints; a drag that starts on the hour column on the left scrolls the page.
- **Inputs** use 16px text so iOS does not zoom the page on focus; fields are at least 40px tall.
- The viewport is `viewport-fit=cover`; the toolbar, bottom bar and dialogs avoid the notch and the home indicator (`safe-area-inset`).
- "Add to Home Screen" (PWA manifest, in the release build). With HTTPS, it could later register as a target of the system share sheet.

Touch devices (any width, `pointer: coarse`): icon buttons are 44px, capsules and tabs taller; there is no hover, so row checkboxes and actions are always shown. The file field for `.torrent` files has no `accept` filter, because iOS does not know the `.torrent` type and would gray out every file; the extension is checked after choosing.

### Add flow

1. The add window opens with its input focused, above the list of what was found and the options. The type is detected as soon as something is pasted: `http(s)://`, `ftp://`, `magnet:?`, `.torrent` URLs; several lines are several items. When a block of text is pasted, every link in it is found ("Found 3 links in the text"). A URL ending in `/` or `.html` is treated as a web page, and adding it lists the download links on the page. File-hosting links get a hint such as "Downloads after signing in with your 1fichier account" or "MEGA is not supported yet"; a Google Drive link says it downloads from Google Drive and that files shared with "Anyone with the link" need no account. The icon on the left switches with the type and plays its entrance once (URL: chain links snap together; magnet: a magnet pulls in particles; torrent: a page corner folds open). Text without a link turns it red and keeps "Start download" disabled, except one line of words (2 to 200 characters) while a search source can be searched: the icon becomes the magnifier, the button "Search", the hint "Press Enter to search torrents for “…”.", the folder options step aside, and Enter or the button closes the window and runs that search on the search page. Several lines without a link stay red.
2. "Add" or Enter opens a **dialog**; folder choice is not on the main screen:
   - URLs (one or more), links found in text, links on a web page: a checklist with quick filters by extension such as "Select all", "Only .iso", and "Select none"; each row is labelled with its type (URL, torrent, magnet, web page, a file-hosting account), and web pages themselves are unchecked by default. Choose "Temporary location", "Move to when finished" and "Site account", then "Start download".
   - **Duplicate detection**: items already in the list (same URL, same name, or a file of the same name and size already at the destination) are marked "Already in the list" and unchecked by default.
   - **The same torrent** (another magnet link or .torrent with the same infohash): no add dialog; instead "This torrent is already in the list", where "Merge into existing task" only adds the new trackers. Torrents with a different infohash but the same content are listed under "Sources (merged)" in the details panel.
   - **Private torrents** are never merged: the same private torrent gets "This torrent is already in the list" with only Close (its trackers are not added), and one with the same files as a task in the list is added as a task of its own, with a note saying so instead of the merge choice. The file list of a private torrent has a note that it finds peers only through its tracker, which may accept only certain clients, and the details Overview shows "Private torrent".
   - **Locations and accounts** (as in the official "New task"):
     - **Temporary location**: where the download ends up when it is not moved when finished. While it downloads, its files are in a private folder at the top of the volume of where it ends up (see Where files go).
     - **Move to when finished**: the first option is "Do not move (stay in the temporary location)". Torrents are handed over as soon as their data is complete and keep seeding.
     - **Site account** (only with URL tasks; not for torrents or file-hosting links): "Auto (look up a saved account by site)" by default, "None", the saved accounts (site and account name), "Enter manually…". Entering manually reveals user name and password fields below, used only for this task and not saved to the account list.
   - Every dialog has "When finished": keep in the list, remove when the download finishes, remove when seeding finishes (torrents only). Only the list entry is removed; files stay.
   - Torrents / magnets: the file list (Select all, Only videos, Select none), the selected size and the free space where the data will be (the volume of "Move to when finished" when one is set, else of the temporary location; it follows both choices), plus the same temporary location and move-to choices. A magnet link must fetch its file list first ("Start download" can still put it in the queue meanwhile); when the account already has three magnet links being looked up, the window says so and asks again by itself every 5 seconds. A torrent file over 16 MB is refused with a note before anything is sent (the NAS reads at most 16 MB of each); several files go up in requests of at most 32 MB.
   - "Temporary location" defaults to the folder used last, then to the default in settings; "Move to when finished" defaults to the default in settings.
   - **Regular users** have no folders to choose: the dialog just says "Saved to home/Download" and has no "Move to when finished"; site accounts list only their own saved accounts.
3. Dropping a `.torrent` anywhere on the page shows a full-page drop hint and, on release, the same dialog. Phones have no drag and drop: tap "+", then "Choose .torrent file", then the same dialog.
4. On success the new task drops into the list from the top as its first row. This is the list's only entrance animation.

### Deleting

Confirmed in an in-page dialog, never `confirm()`. The option "Also delete downloaded files" is unchecked by default. A delete can be undone for 6 seconds, except when files were deleted too. For unfinished URL tasks the temporary files are always removed, whatever that option says.

### Where files go

- Every task, administrators' too, downloads into a private folder at the top of the volume its destination is on, `@DownloadCenterTemp/<hash>/`, which File Station does not show and users cannot reach. Its files appear in "Move to when finished" (or in the temporary location when nothing is set) only once complete, so half-downloaded files never show up; a name that is taken gets " (1)". The details panel's Overview shows "Files being downloaded" (`@DownloadCenterTemp/<hash>/…`) while downloading and "File location" once finished; Open folder shows the destination meanwhile.
- Torrents are handed over into the destination as hard links and keep seeding from a private copy (`@DownloadCenterTemp/<hash>.seed`). Deleting or replacing their files in the destination while they seed does not change what is seeded, and frees no space until the task leaves the torrent engine or seeding ends. Files selected after a torrent finished appear in the destination only once complete.
- Adding a torrent whose files are already at the destination continues with them only when they match it: each file must be a plain file of the expected owner (not a link, no other names), and every whole piece of the torrent it holds must verify (blocks of one repeated byte do not count as a match). A file that is only shorter than in the torrent is completed. If any file differs, none of them is used and the torrent downloads as new (under "Name (1)" when the name is taken). Files that are used are taken into the private copy without being moved. The task log says "Checking the files already in the destination folder" and then "The files already in the destination folder are used".
- After the upgrade to 1.4.0, unfinished tasks are moved into the private folders once and torrents are checked again; torrents that were seeding from their destination keep seeding when their files are unchanged, taken over the same way; a copy that was changed after it was handed over is not repaired, and that torrent's seeding ends.
- The help text of Settings › Download › Folders spells this out; regular users see the version about their own home folder.

### Search

Searching for torrents asks the search sources an administrator set up (built-in sources of freely distributable content, Torznab servers, plugin files) and hands a result to the usual add window; it never adds anything by itself. The page is `#search` (the keyword is never in the address).

**Who sees it**: every account with a source that can be searched sees Search in the sidebar (between the filters and Settings) and in the phone's bottom bar. With no source on, administrators still see it, and the page says "No search source is turned on yet" with "Open Settings › Search"; regular users do not see it. `Ctrl+K` (`⌘K` on a Mac) opens the page and focuses the field from anywhere outside an input field or dialog.

```
[🔍] wikipedia_____________________________________________ (×) [ Search ]
     Press Enter to search torrents for “wikipedia”.
[All][Video][Music][Software][Books][Other]
(● Internet Archive 24) (● Kiwix 7) (◌ My Prowlarr) (● Fedora  Timed out)
Results  31 so far                                  [Stop]  Sort [Seeders▾] [↓]
══════════                                                  (a thin line sweeps while searching)
(((•)))  wikipedia_en_all_maxi_2026-09.zim                                   (+)
         214 seeders  38 downloading  109 GB  2026-09-14  My Prowlarr, Kiwix
(☁↓)     Wikipedia Audio Articles, spoken English collection                 (✓)
         Direct download source  3.1 GB  2021-02-02  Internet Archive  [In the list]
Search sources are third-party sites. Download only what you have the right to download.
```

- **The field** is a glass capsule with a magnifier, a clear button and "Search"; it takes the focus when the page opens. It also knows links: a pasted URL or magnet link turns the magnifier into a chain and the button into "Add", which opens the add window with that link instead of searching. The add window does the opposite for words (see Add flow), so neither entrance can be the wrong one.
- **Before searching**: one line on what the page does, the sources it asks ("Searches Internet Archive, Kiwix, Fedora."), and the last 8 keywords as capsules with "Clear recent searches". They are kept only in this browser, per account.
- **Category**: a segmented control (All, Video, Music, Software, Books, Other). The category is sent to the sources; after a search of all categories, switching filters the results in the page, while switching away from a category the sources were asked for asks them again.
- **Sources answer one by one**: one capsule per source, with a turning circle while it is asked, then a green dot and the count, or orange with the reason (Timed out, Could not connect, The server refused the API key, Could not read the answer, This address is not allowed, The source is busy); a failed capsule opens a line with the whole reason. A done capsule filters the list to that source (pressed, accent), again to show all. The capsules are a polite live region; on phones they scroll sideways.
- **While searching** a thin line sweeps over the top of the list and the heading has "Stop". That line and the capsules' circles are the page's only spontaneous motion; new rows fade in once (0.45 s, a few pixels down). Reduced motion stops all of it.
- **Results** use the task list's group and row grid: a signal in a tinted circle that answers "can I get it?" in the source status vocabulary (10 or more seeders green, 1–9 orange with the outer wave faded, 0 red and struck through, a site that serves the file itself teal with a cloud and "Direct download source", unknown gray with the seedling), the title (two lines at most), one line of details (seeders and downloading when known, size, date published, the sources that found it; unknown items left out) and a round accent "+" at the end. A direct download source wins over a few seeders: the site serves the file whatever the peers do.
- **Order**: Relevance (each source's own order, taking the sources in turn), Seeders, Size, Date published, Name, with the direction arrow; Seeders is the default once any result has a seeder count, otherwise Relevance. Results arriving later go to their place in the order while the row under the pointer or with the focus stays where it is on the screen. 50 rows first, then "Show 50 more".
- **Details**: clicking a row (not its button) opens a card under it: the full name, which sources found it, the category, the date, the info hash with a copy button, and "Open source page" (a new tab, `noopener noreferrer`, only for http and https addresses). Every text of a result is plain text.
- **Adding**: "+" opens the add window. A result with a magnet link goes in as if that link were pasted (the file list comes first); a result with only a `.torrent` is fetched through the NAS first (the button turns into a spinner) and then goes in as if that file were chosen; a source that answers with a magnet link instead is handled like one. Failures are a toast with the reason: two `.torrent` files are already being fetched for this account (at most two at a time), the results are no longer available (the search ended or its source was switched off), or the source gave no valid `.torrent`. After adding, the page stays and the row says "In the list" at once. A result whose info hash is already in the list says "In the list" and its button (a check) opens that task's details. Accounts without permission to add see no "+".
- **Nothing found**: "Nothing found for “…”. Try fewer words or another category."; when every source failed, that the reasons are on the capsules; a category filter with nothing in it offers "Show all categories".
- **Leaving and coming back** keeps the field, the filters, the order and the results without asking again, until the results expire (half an hour after the search ended) or the page reloads. A search still running when the page is left finishes in the background.
- **Footer**: always "Search sources are third-party sites. Download only what you have the right to download."
- **Phones**: the bottom bar has Tasks / Search / Settings and "+". On the search page the tabs fold into one round glass button on the left (the symbol of the tab the page was opened from; it goes back there) and the rest of the bar becomes the focused search field, under the thumb and above the keyboard. The category control and the source capsules scroll sideways at the top, results run down under them, and "+" on a row is 44px.
- **Narrow windows** (the QTS desktop included) follow the same container breakpoints as the rest of the app.

## Settings

Wide screens show one page with tabs. Phones first show an iOS Settings style list of sections; tapping one opens it, and "‹ Settings" at the top left goes back.

**List-type settings are always added and edited in a window** (2026-10-02): users, logins, file-hosting accounts, access tokens, notification channels, adapters and proxies alike.

- When a list is empty, the card shows an icon, one line "No … yet" and a primary "+ Add …" button in the middle to lead to the first entry; once there is data, the button moves to the bottom right of the list.
- Adding and editing use the same window: title "Add … / Edit …", primary button "Add (Create) / Save". Each row has "Edit" (pencil) and "Delete" on the right, with other actions (verify again, send test notification, regenerate) between them.
- Settings saved as a whole page (Download, Schedule): when something changed, a save bar pinned to the bottom of the window shows "Unsaved changes" and "Revert"; switching tabs, leaving settings or closing the page with unsaved changes asks first.
- Passwords, keys and tokens are never sent back: when editing, the field is empty and its help says "Saved; leave empty to keep it". Editing a token changes only what it may do, never the token itself; the expiry defaults to "Keep the current expiry".
- A new notification channel starts by picking the service in the window, then its fields, with "Choose another service"; when editing, the service is fixed. File-hosting accounts cannot change service either.
- On phones the window is a panel sliding up from the bottom, fields always stacked with full-width controls, buttons pinned to the bottom.

| Tab | Contents | Regular users |
|---|---|---|
| Download | Default temporary location and move-to folder, finished tasks (default for removing them automatically), concurrent downloads, speed limits and the values for limited hours, torrents (torrent engine, ports, UPnP (libtorrent only), DHT/LSD/PEX, seeding conditions, incoming port test), torrents that find no peers (the public tracker list for new torrents and its state, when a torrent counts as not shared (hours, default 6) and as probably no longer shared (days, default 3), with a note when the days are shorter than the hours), proxy servers (HTTP; SOCKS5 as well when the torrent engine is libtorrent), sign-in (an explanation of the QTS rules, direct sign-in from the QTS desktop) | Only "Files are saved to home/Download in your home folder, owned by you" |
| Users | Who can use Download Center is set in QTS (Control Panel › Privilege › Users › Edit Application Privilege), so this page explains where, has a button that opens QTS there (the desktop's Control Panel inside the QTS desktop, the QTS desktop in a new tab otherwise) and lists the accounts that can use it now, administrators (QTS administrators) first, with the groups QTS allows. A reminder appears when the home folder service is off | — |
| Schedule | 7×24 drag-to-paint: full speed, limited, paused; clicking a weekday paints the whole day, clicking an hour paints it across the week; a plain-language summary below | — |
| Search | The search sources (see below) | — |
| Site accounts | Logins (HTTP/FTP sites) and file-hosting accounts (1fichier, Rapidgator, Real-Debrid, AllDebrid, cookies for other sites). Verified file-hosting accounts show their plan, expiry date and traffic left today, and can be verified again; MEGA and free downloads that need a captcha are not supported | Their own |
| Access tokens | See below | Their own |
| Notifications and integration | See below | Their own |
| Import from the official package | What was detected and checkable items (settings, unfinished tasks, finished history, site accounts), then stopping the official package, importing and verifying. Shown only when official data is detected | — |

Features the current engine cannot do get no switch and no "not supported" text. A one-line instruction appears only when users must do something themselves (for example, without UPnP in the engine, the incoming port help says "Forward these ports on your router to the NAS"). Options follow the engine's capabilities, never a hard-coded engine name: proxy types are "None / HTTP / SOCKS5", and SOCKS5 adds "Resolve domain names through the proxy" and "Torrent connections". "Resolve domain names through the proxy" and "Proxy only" are on by default. Since aria2 was removed on 2026-10-02 there is no torrent engine choice any more; when this NAS cannot download torrents or FTP, one line under the matching concurrent-downloads field says why.

"Test incoming ports" requires checking "Allow an external service to test incoming ports" first (off by default, because it tells an outside service the NAS's public IP and ports); the result says in one sentence which ports are reachable and which need forwarding on the router.

### Search sources

Administrators, in the web UI only (tokens cannot manage sources).

- The section starts with what a source is: a third-party site; built-in sources, Torznab servers and plugin files only describe how to ask a site and cannot run code; plugins from another NAS are programs that run in a restricted environment, only after an administrator agrees to the terms; searches go out from the NAS and never to the NAS itself or its local addresses.
- **List**: a colored square (blue built in, purple custom plugin, teal Torznab, a gear for a plugin from another NAS, gray when off), the name with a "Built in" / "Custom" / "Torznab" / "Synology .dlm" / "QNAP .addon" capsule, an orange "Runs code" capsule for plugins from another NAS, the version of a custom plugin or a plugin from another NAS ("v1.05"), "Needs setup" in orange when a required setting is empty, the description, "Installed by <user> on <date>, who accepted the terms" for a plugin from another NAS, "Connects to <host>" and "Test", which asks the source once with its test keyword and says "Found 12 results in 0.8 s." or the reason it failed. A plugin from another NAS that this NAS cannot run says "Not compatible with this NAS: <reason>" in red, has no "Test" and cannot be switched on. Every source has "Who can use it": "Everyone" or "Administrators only", saved at once; opening a source whose key or account is set to everyone first asks "Let everyone use …?" (its links can carry what identifies the account on the site, such as a private tracker's passkey). At the end: "Edit" for sources with settings, "Export" (the plugin file) and "Delete" (confirmed in the page) for custom ones, "Delete" for Torznab ones and plugins from another NAS, and the switch, saved at once.
- **+ Add source** (under the list) and "For developers:" with the plugin format document under the card.
- **Add source** is one window whose content follows the steps: first the kind, "Torznab server" (Jackett, Prowlarr or another Torznab server on the network), "Plugin file" or "Search plugin from another NAS" (a Synology .dlm or QNAP .addon file: a program that runs on this NAS; when this NAS cannot run such plugins, the choice says so with the reason).
  - Torznab: name, server address and API key, a note that the server cannot run on the NAS itself or on a loopback address, then "Test" (shown in the window, nothing saved) and "Add" (switched on at once; the key is never shown again).
  - Plugin file: "File" / "Paste JSON" / "From an address" (https only), then "Continue", which has the NAS check it. Errors stay in the window, next to what caused them ("The request address must use https"). A valid plugin shows the review card before anything is installed: name and version, description, the host it connects to, what it sends (the search words and the settings, nothing about the account or the NAS), the headers it adds, the note "This plugin is a description, not a program. It cannot run code or reach this NAS.", the settings to fill in, and "Use it for searches right away" (on). A plugin with the id of an installed one says it replaces it, and either that it keeps its settings (same type, request address, link fields and settings; no on/off choice then) or that it starts without its saved values and switched off (the on/off switch is shown, off); the button is then "Replace". An id that belongs to a plugin from another NAS is refused already when checking, with what to do.
  - Search plugin from another NAS: only a chosen file (.dlm or .addon); the NAS checks it without running anything. When this NAS cannot run such plugins the window says "This NAS is not compatible with this plugin" with the reason underneath (for a missing PHP also: "The built-in PHP comes with the QTS web server: turn on Web Server in QTS Control Panel › Applications, then try again.") and offers "Choose another file". Otherwise it shows the review: name and version, the format, the sites it declares, whether it signs in with an account, an add-on's signing key with the note that the signature only shows the file is intact and which key signed it, and warnings when it replaces an installed plugin or an add-on signed with a different key. Then the terms, "This plugin runs as a program on this NAS": it is a program written by a third party; Download Center has not reviewed it and is not responsible for what it does, the sites it contacts or what it finds; it runs in a restricted environment that keeps it away from your files and your local network, but no restriction is a guarantee; it can send your search words, and any account you give it, to any site on the internet; install only plugins from a source you trust. "Install" stays disabled until "I understand and agree; I am responsible for installing it" is ticked; the focus starts on "Cancel", and while the terms are shown the window closes only with "Cancel" or "Install" (not with Escape, a click outside or pulling the sheet down). The plugin is installed switched off, for administrators only; a replacement is too, and its saved account is dropped, to be entered again for the new code.
- **Edit**: the source's settings; secret ones are empty with "Saved; leave empty to keep it"; a Torznab source can be renamed. A plugin from another NAS that signs in has "User name" and "Password" and "Save and verify account", which saves the account and asks the plugin whether the site takes it ("The site accepted the account." or the reason).
- Tokens: the custom permissions have "Search for torrents" (`search:use`); none of the presets includes it.

### Access tokens

- List: name, `dct_<id>_…`, permission tags ("Remove tasks and delete files" in vermilion), expiry date, last use time and IP; can be regenerated or revoked. Revoking asks for confirmation.
- Create: name → permission preset (read only / add downloads / full control (without deleting files) / custom, which expands to per-permission checkboxes) → which tasks it may act on (mine / everyone's, the latter for administrators only) → allowed folders → allowed IPs → expiry.
- After creation the full token is shown once, with a "Copy" button. Once closed, the full token cannot be seen again.
- "For developers" at the bottom: API address, authentication, where the documentation is.
- **AI Agent** section: the three steps for a token and the skill file, then the switch "Offer tools to AI agents in this browser (WebMCP)" with one line on whether this browser offers WebMCP (or that it needs HTTPS, or how to turn it on in Chrome). With it on, the page registers nine tools with the browser (`js/webmcp.js`): read the overview, list and inspect downloads, add, pause/resume/retry/reorder, choose a torrent's files, add trackers to torrents that find no peers, delete, and show something in the page. The agent acts as the signed-in user; the first small change in the page goes through, every further change asks in the page first, and so does any change with more than 10 items, a folder, trackers of its own or links that lead to the NAS or the local network (also wrapped in thunder://, flashget:// or qqdl:// links, or as a magnet link's trackers, web seeds or peers, which the question lists under “Addresses on this NAS or your local network”, showing wrapped links as the link they carry); every request a tool sends carries `X-DC-Via: webmcp`, set by the page, so the NAS checks what an agent adds like a token's adds (no connections to the NAS itself, whatever the name) and names the AI agent as the caller; deleting always asks; the question lists everything in full (it scrolls), starts on Cancel and never opens over another dialog; every change shows a toast such as "AI agent paused 3 downloads". The setup command for an agent is shown only when the page's address is an ordinary host name or IP address with an optional port; over plain HTTP the section warns that the token and the skill file travel unencrypted.

### Notifications and integration

Notifications and chat control are one bot channel, not two features.

- **Channel list**: service and name, which events it reports, quiet hours and how messages are batched. Each row has a "Control downloads in the channel" switch:
  - Telegram: can be turned on.
  - LINE: can be turned on when the NAS is reachable from outside over HTTPS.
  - Discord, Slack, ntfy and the like: the switch is disabled with "This service can only receive notifications".
  - Webhook: notes that your own service calls `/api/v1/commands`.
  - When on, it shows "N users linked" and "Link my account", which generates a channel-specific `/link 123456`, valid for 10 minutes.
- **Add channel**: pick the service, fill in its fields, the events to report, "Control downloads in the channel", scope (administrators can choose everyone's tasks), quiet hours, batching. A channel with control enabled shows its pairing code right after saving. Accounts that are not administrators have up to 10 channels; with 10, "Add channel" gives way to a note saying so.
- **For developers**: the chat command endpoint, event stream, webhook signatures, adapters.

## Inside the QTS desktop

The package installs as a desktop app (`QPKG_DESKTOP_APP=1`); QTS loads the same address in an iframe inside a desktop window. Default window size 1100 × 720 (`QPKG_DESKTOP_APP_WIN_WIDTH` / `_HEIGHT`), resizable by the user.

- **Detection**: inside an iframe (`window.self !== window.top`) the page gets the `embedded` class. Whether QTS passes a recognizable parameter or referrer is to be checked on the NAS during implementation, as a second signal.
- **No duplicate title bar**: the window title bar already shows the icon and "Download Center", so when embedded the page hides its own brand; the top bar keeps only speeds, schedule and account, plus an "Open in new tab" button. Margins shrink and the content width has no maximum.
- **Sign-in**: the QTS session is reused, the sign-in page never shows, and the account menu has no "Sign out" but explains signing out from QTS instead. When the QTS session has expired, one line of explanation and "Reload" appear.
- **Window size**: layout breakpoints are computed on the box the app lives in (CSS container queries), so a narrowed QTS window switches to the top filter bar and bottom-sheet dialogs like a phone, and below 560px it is the full phone layout (bottom bar included); in a normal browser tab they follow the browser width. Where JS needs to know about the phone layout, it measures the same box, not `matchMedia`.
- **Dialogs, details panel and toasts** stay inside the window and never spill outside the QTS window.
- Dragging a `.torrent` from the file manager onto the QTS window adds it as well.
- "Open folder" inside the desktop should open a QTS File Station window; how to call it is to be checked on the NAS, and if that is not possible, File Station opens in a new tab.

## Deskpane widgets

Deskpane (an alternative web desktop for QTS) lets installed packages put widgets on its desktop and its phone home screen. Download Center declares two in `shared/deskpane/widgets.json` (generated by `mklang` from `daemon/cmd/mklang/widgets.json`, with every UI language); without Deskpane the file does nothing. Deskpane owns the look: its glass, colour tokens, type scale and, for data widgets, the templates. What Download Center designs is the content, and one moment of its own.

| Widget | Kind | Sizes | Options | Content |
|---|---|---|---|---|
| **Download queue** (`queue`) | data: Deskpane draws `GET /api/v1/widget/queue` | desktop S, M, L; phone S, M | Show: In progress / All downloads | The download speed (hidden when nothing downloads), "3 downloading, 2 waiting" and the progress of what is downloading; one status sentence only when something needs a look (a failed download, low space, the schedule pausing or limiting, an engine down for administrators, or "All downloads finished"); the tasks in order of importance (downloading, failed, waiting, paused, then finished with All downloads), each opening its details; L adds the download speed of the last 15 minutes |
| **Add download** (`add`) | module: the custom element `downloadcenter-add` from `widgets/add.js` | S, M on both | Start right away (on) | S: Download Center's three symbols (URL, magnet, torrent), "Drop a URL or .torrent file here" and a field. M: the field with Add, the hint, and the last two results. Drop URLs, magnet links or `.torrent` files on it, or paste and press Enter; on a phone a Paste button reads the clipboard (HTTPS only). Results say Added / Already in the list / why it failed, and open the task |

- **The one bold thing**: while something is dragged over Add download, the whole pane gets an accent edge and the three symbols play their own animations (the chain snaps, the magnet pulls, the file folds); "Drop to add" replaces the content. Reduced motion keeps the edge only. Everything else is still: data updates do not animate.
- **The hero answers "what is going on"**: the speed while something downloads; otherwise a sentence or the list, never "0 KB/s" for an idle queue.
- **Links into the app**: every path a widget opens carries a `#` (`#task/<id>`, `#tasks/error`, `#tasks/down`, `#tasks`), so a Download Center window that is already open in Deskpane switches page instead of reloading. `#task/<id>` opens the list and that task's details once the list has arrived, and stays in the address while the details are open.
- **Language**: both follow the language chosen in QTS like the rest of the UI (Deskpane itself only tells widgets English or Traditional Chinese); the document is translated by dcd, the module loads `widgets/lang/<code>.json`.
- **Resources**: Deskpane fetches the document only while the widget is on screen (every 15 s while something downloads, else 60 s); Add download never polls. The speed history is kept in dcd's memory, per user.

## Color themes

Three options: **Light**, **Dark**, **Auto** (default, following the device's light or dark setting).

- Where: "Appearance" in the account menu, and at the bottom of the sign-in page so it can be chosen before signing in.
- A per-user preference stored on the server (user settings). The browser keeps a copy too, applied before the page paints on the next visit so the other theme never flashes first.
- Implementation: every color is a CSS variable. Light is defined on `:root`; dark is written both under `@media (prefers-color-scheme: dark)` (excluding `[data-theme="light"]`) and under `[data-theme="dark"]`. Choosing Light or Dark sets `data-theme`; choosing Auto removes it. The dark set uses Apple's dark system colors (background `#000000`, lists `#1C1C1E`, accent `#0A84FF`), with the dark variants of all state colors to keep contrast.
- The switch uses three animated icons: a sun (rays turning), a moon (a tilt and a star twinkling once), a half circle (turning to the other side).
- The same place has the "Glass" slider, from clear to frosted, setting the opacity of every glass layer.

## Icons and animation

24×24 inline SVG, stroke width 1.7 (SF Symbols regular), round caps, `currentColor`. CSS keyframes act on SVG children (`transform-box: view-box`, `stroke-dashoffset`).

| Kind | When it plays | Rule |
|---|---|---|
| State loop | While the state lasts | 1.2–3 s period, small amplitude |
| One-shot transition | The moment the state changes | 0.4–0.7 s springy curve, then still; row nodes are reused, so it replays only when the state really changes |
| Action response | Hover, keyboard focus, `.play` (type detection) | 0.3–0.8 s |

Icons in the filter bar, the brand and empty states stay still and move only on hover.

**Search results** use the source status signal still (no loop): full waves, the outer wave faded, struck through; a cloud handing a file down for a direct download source; the seedling when nothing is known.

**State icons**: source status still looking (the waves fade in turn), probably dead (the struck-through waves shake once); downloading (an arrow drops into a tray), waiting (clock hands sweep), paused (two bars spring apart, then rest), seeding (a sprout sways, ripples spread), finished (a checkmark draws itself), checking (a magnifier scans), moving (an arrow slides into a folder), error (an exclamation mark shakes once).

**Action icons**: add (turns 90°), URL (chain links snap together), magnet (the magnet swings and pulls particles in), torrent file (a corner folds open), start (pushes right), pause (squashes and springs back), delete (the bin lid lifts), move up / move down (bounce), folder (opens), schedule (a calendar page flips), speed (the needle swings), site accounts (a key turns), settings (the gear turns), choose files (checks in sequence), retry (one full turn), details (dots hop), import (an arrow drops), access tokens (a ticket tilts), notifications (a bell rings), chat commands (typing dots hop), webhook (a plug goes in), copy (sheets shift apart), sign in (a lock opens).

**Reduced motion**: with `prefers-reduced-motion: reduce` all animation stops; states stay distinguishable by shape and color.

## Copy principles

- Verbs say what will happen: "Add", "Start download", "Pause", "Delete", "Open folder". The button is "Delete", so the toast says "Deleted 2 tasks".
- Errors give the cause and the next step: "FTP sign-in failed (530). Check the login for backup.lan in Settings › Site accounts."
- Empty states are invitations: "No downloads yet. Paste a URL above, or drag a .torrent file into this window."
- Say "torrent" and "URL" to users, not "BT" or "HTTP"; terms such as DHT and PEX appear only in the torrent settings, each with a plain-language line.

## Accessibility

- Keyboard: `/` opens the add window, `Ctrl+K` (`⌘K`) the search page with its field focused, Tab to a row then `Enter` opens details, Space checks it, `Esc` closes a dialog or the drawer.
- Every icon button has an `aria-label`; state icons have a `title` and always sit next to text.
- Text contrast ≥ 4.5:1; states differ in shape as well as color.
- Touch: tap targets at least 44 × 44px; on phones the main buttons are in the lower half of the screen; every gesture (long press to select, pull down to close) has a tappable alternative ("Select", a close button).

## Prototype coverage

| Built | Described here only |
|---|---|
| Every sign-in branch: admin (2-step, code 123456; can switch to backup email or the security question, answer "小白"), alice (signs in directly), expired (password expired), enrol (must set up 2-step first), the password "wrong" for a plain failure, any other account for "not on the list"; the "Sign in with the QTS page" link; sign-out explaining that QTS signs out too; the verification countdown; don't verify again on this device; the http warning; show password. Sign out, switching between the admin and alice views, the user list | Real calls to `authLogin.cgi`, QTS blocking, direct sign-in from the QTS desktop |
| Paste detection, link extraction from text and web pages, extension filters, duplicate detection, merging the same torrent, file-hosting link hints, the add dialog, file selection, automatic removal when finished | Actually switching between sources of different infohashes with the same content, real free-space numbers |
| Preview in the details panel (media, archives, not previewable), the watch-while-downloading switch, merged sources | Actual playback |
| Settings: incoming port test, file-hosting accounts, notification bot (with the control switch and pairing code) | Real connections to outside services, file-hosting sites or chat services |
| QTS desktop window simulation (from the account menu or the footer, with window width switching and "Open in new tab"), Light / Dark / Auto | Whether QTS passes an identifying parameter, opening a File Station window from the desktop |
| Live list simulation (queue, seeding, moving, checking), piece progress bar, bulk actions, delete and undo, details drawer | The pause↔resume icon morph (the prototype swaps icons and plays one transition) |
| Phone layout: one-line toolbar, filter bar (empty filters hidden), bottom tab bar and the "+" add panel, the main action at the end of rows, "Select" and long-press selection, the bottom bulk bar, dialogs and details panel closed by pulling down, the settings section list, the turned schedule grid. The width buttons of the QTS window simulation can switch to phone width | Add to Home Screen (PWA), share sheet, keyboard avoidance and clipboard on real devices |
| Every settings tab, schedule painting, creating and revoking tokens, notification channels, pairing codes, switching the torrent engine (including what happens to existing torrents), proxy servers (HTTP or SOCKS5 by engine, scope, proxy only, proxy test), the add dialog's temporary location / move to when finished / site account (including manual entry) | Actually sending notifications, the event stream, import steps 2 and 3, real proxy connections |
| Icon gallery | — |
| — | Torrent search: the search page, the add window's search mode, the Search tab of the phone bar and Settings › Search. They are not in the prototype (which still has the earlier paste bar on the main screen); the real UI is the reference |
