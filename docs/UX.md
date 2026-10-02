# Download Center UX/UI design

Clickable prototype: `docs/prototype/index.html` (sample data, no backend, ES5, portable to `shared/web/`). What the prototype covers is listed at the end.

UI copy is written in Traditional Chinese, which is the translation source; this document quotes the English UI strings.

## Audience and main tasks

- **Audience**: NAS users at home or in a small office, including regular users, not only administrators; often on a phone. Most do not know BitTorrent terms and do not want to pick "what kind of download this is" first.
- **Main tasks** (by frequency):
  1. Paste one or a batch of URLs or magnet links, or drop a `.torrent` in.
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

1. **Floating glass layers**: the sidebar, toolbar, paste bar, bulk-action bar, dialogs, details panel and toasts are translucent glass (30px blur, 180% saturation, a thin highlight on the top edge, soft shadow), kept apart from each other and from the window edges so they float over the content.
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

- **Sidebar**: floating glass panel, 22px radius; the selected item has a gray capsule background, icons use state colors. In narrow windows it becomes a horizontally scrolling capsule bar at the top; on phones it leaves out Settings and hides filters with no tasks.
- **Bottom bar** (phones only): a glass capsule tab bar "Tasks / Settings" with a round accent "+" add button beside it; in selection mode it turns into a labelled bulk-action bar.
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

- With a valid `NAS_SID` when the page opens, the sign-in page is skipped; when `NAS_PW_STATUS` says the password must change, the "password expired" screen is shown directly.
- "Sign out" in the account menu also signs QTS out in this browser, and says so next to the button.
- The account menu is a popover under the avatar on desktops (no dimming) and a bottom sheet on phones; an appearance choice applies and is remembered immediately, without "Save". The toolbar avatar, like the one in the menu, is the QTS profile picture, or the first letter of the account name when there is none.
- **Terms of use**: shown once per account at first sign-in (cannot be closed by clicking outside or `Esc`, only with "I understand and agree"). It says the software only downloads links the user provides, to download only content one has the right to, that BitTorrent shares data and exposes the IP address, who is responsible, the disclaimer, and that it has nothing to do with QNAP. The consent is stored in the user's settings; the text can be read again from "Terms of use" in the settings footer. The footer also has "Licenses" (the third-party license notices shipped in the package).
- **Updates** (administrators only): when a new version exists, a small blue tag "New version x.y.z" appears in the toolbar (icon only on phones); nothing pops up. Opening it shows the release notes as plain text with "Skip this version", "Later" and "Update now". During an update, progress is shown (download, signature check, database backup, install); after the package restarts, the page reloads by itself and reports the result once. Settings › "About and updates" lists every release, each with update or "Go back to this version" (a dangerous button, focus starts on Cancel; across database formats it explains which day's backup will be used and that later changes will be lost).
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
| `authPassed=1` | Then the user list is checked: listed users go in; others stay on the sign-in page with "This account has no access to Download Center yet. Contact your administrator." |
| `user_pw_expiry=1` or `pw_status=1` | "The password of this account has expired. Change it before signing in." with a "Change password in QTS" button (opens the QTS sign-in page in a new tab). The package does not change passwords itself |
| `need_2_step_verification=1` | 2-step verification screen (below) |
| `force_2sv=1` | "Your administrator requires 2-step verification for this account, but it is not set up yet." with an "Open QTS" button |
| Anything else (wrong password, disabled or expired account, blocked by QTS) | In QTS's words, a single sentence "The login credentials are incorrect or the account is no longer valid." that does not reveal which one it was |

**2-step verification**:

- The default is the authenticator's 6-digit code: one field (`inputmode=numeric`, `autocomplete=one-time-code`, so phones can fill it from a text message or the authenticator) that submits by itself once 6 digits are in.
- A wrong code shows the NAS's current time: "The code is incorrect. Try again. The NAS time is 09:41; your authenticator's clock must match it." An out-of-sync authenticator clock is the most common cause.
- A countdown at the bottom left (3 minutes for the authenticator, 5 for backup email and security question, as in QTS); when it runs out, the page returns to the first step with "Verification timed out. Sign in again."
- "Verify another way" lists the methods set up for this account: authenticator, send a code to the backup email (shown masked), answer the security question. After the email is sent, its code goes into the same field; too many wrong security answers disable the method and ask the user to contact the administrator.
- A "Don't verify again on this device" switch maps to QTS's `dont_verify_2sv_again`; the vtoken QTS issues is kept in this browser (localStorage) as the QTS sign-in page does, so the next sign-in on the same device skips the second step.
- Every screen has "Use another account" to go back to the first step.

**Other rules**:

- Opening `https://<NAS>:<port>/DownloadCenter/` lands on this page; opening from the QTS desktop reuses the QTS session and skips it.
- On load, the page asks QTS whether HTTPS is enforced: if so, it goes straight to the same page on `https://` (the browser must share an origin with QTS's sign-in CGI to call it); otherwise, when opened over `http://` (except `localhost`), a warning line with "Use encrypted connection" appears at the top.
- While submitting, the button reads "Signing in…" and is disabled to prevent double submits.
- **Repeated failures** are QTS's business; the package keeps no count. QTS gives no reason when it blocks, so the screen shows the same failure sentence.
- "Remember user name" keeps only the user name in this browser, never the password. There is no "stay signed in"; session lifetime follows QTS.
- The password stays only in the sign-in page's memory, because the second step must send it to QTS again; it is cleared on success, timeout or leaving the page.
- "Sign in with the QTS page": appears only if QTS redirect sign-in (`redirect_uri`) works in testing, so QTS handles QNAP Authenticator, SSO and other methods the package does not.
- The eye button at the right of the password field shows or hides the password (passwords are easy to mistype on phones).
- The user name and password fields carry `autocomplete=username` / `current-password` so browsers and password managers can fill them.
- Light, Dark or Auto can be chosen at the bottom of the sign-in page too, before signing in.

### Main screen (desktop)

```
Download Center                     ↓ 18.6 MB/s  ↑ 1.2 MB/s  ◷ Full speed, limited from 18:00  admin
──────────────────────────────────────────────────────────────────────────────────────────────────
All          9     [⛓] Paste a URL or magnet link _____________________________ [tor] [ Add ]
Downloading  3     Try a URL  Magnet link  Several URLs   You can also drag .torrent files into the window
Waiting      1
Paused       1     All  9 tasks
Seeding      1     ──────────────────────────────────────────────────────────────────
Finished     1     ↓  ubuntu-24.04.3-desktop-amd64.iso
Error        1        ▮▮▯▮▮▮▯▮▮▮▮▯▮▮▮▮▮▯▮▮▮  62%  3.66 GB / 5.90 GB  9.2 MB/s  4 min left
                   ──────────────────────────────────────────────────────────────────
Settings           ↓  LibreOffice_25.8.1_Linux_x86-64_deb.tar.gz                    alice
                      ─────────────────────                     41% …
```

- Sidebar filters have state-colored icons and counts; the current one has a gray capsule background and bold text.
- **Sort**: a "Sort" menu at the right of the list title: queue order (default), state, progress, time left, time downloading; the arrow beside it flips the direction, and each sort has a sensible default direction (state: what needs attention first, error → downloading → checking → moving → seeding → waiting → paused → finished; progress: high to low; time left: short to long, tasks without one last; time downloading: long to short).
  - The values change every second, so a sorted list reorders at most every 10 seconds to keep rows from jumping under the pointer.
  - Sorting by progress or time downloading shows that value on the rows; time downloading counts only time actually spent downloading.
  - Move up and Move down work only in queue order; with other sorts the bulk bar hides those two buttons.
  - The sort choice is a per-user preference, kept for the next visit.
- Row checkboxes appear on hover; once something is checked, the list title turns into the bulk-action bar (start, pause, move up, move down, delete, cancel).
- The main action at the end of a row (pause, resume, retry, open folder) is always shown; the "Details" button appears on hover. Row details (percentage, size, speed, time left) are laid out separately; more than 30 days left shows "More than 30 days left".
- Clicking a row opens the details drawer on the right: Overview (merged sources are listed here), Files (choose what to download), Preview, Connections (BT), Log.
- **Preview**: media gets a player, and its progress bar separates the contiguous part that can be played from what is downloaded but not contiguous; torrents can switch on "Watch while downloading" to download in order; archives list the directory as far as it can be read; disc images and other types that cannot be previewed say why.

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
- The **bottom bar** follows the iOS 27 Liquid Glass tab bar: a glass capsule with the Tasks and Settings tabs on the left and a separate 60px round accent "+" on the right. Tapping Tasks again scrolls the list back to the top.
- **Adding**: the main screen has no paste bar. "+" raises the "Add download" panel from the bottom with the input focused and the keyboard up, ready for a long-press paste; when the page is on HTTPS there is also a "Paste clipboard" button (browsers allow reading the clipboard only over secure connections). Below it is "Choose .torrent file". "Add" then switches to the same add dialog as on desktops. "Add" is disabled while the pasted text holds no link.
- **Rows**: only one main action at the end (pause, resume, retry, open folder), as a 44px round button; the "Details" button is gone, tapping anywhere else on the row opens details. Downloading rows show only percentage, speed and time left; downloaded size is left to the details panel. Rows get a background on press.
- **Multiple selection**: "Select" next to the list title; a long press on a row also enters selection mode and checks that row. In selection mode:
  - Round checkmarks appear at the start of rows; tapping a row checks or unchecks it instead of opening details.
  - The title bar becomes "Select all / 3 selected / Done".
  - The bottom tab bar turns into the bulk-action bar: start, pause, move up, move down, delete, each with a label under its icon; disabled while nothing is checked.
  - "Done" or a delete leaves selection mode.
- **Dialogs** rise from the bottom, 6px from the sides and bottom. Title and buttons stay put and only the middle scrolls, so however long the file list, "Start download" stays under the thumb; buttons are full width and 50px tall. Pulling the grabber or the title down closes it. The dialog moves up with the keyboard instead of being covered. In the add dialog, "Temporary location / Move to when finished / Site account / When finished" put the label above the menu.
- The **details panel** is also a nearly full-screen sheet rising from the bottom, closed by pulling down; the first footer button is the main action (pause, resume, retry, stop seeding), followed by open folder and delete.
- **Settings** follows the iOS Settings app: first a grouped list (colored icon, name, a one-line description, ›); tapping one opens that section, and "‹ Settings" at the top left goes back. No row of seven tabs scrolling sideways.
- The **schedule** grid turns on phones: weekdays across the top, the 24 hours running down, so seven days fit without sideways scrolling. A drag that starts on the grid paints; a drag that starts on the hour column on the left scrolls the page.
- **Inputs** use 16px text so iOS does not zoom the page on focus; fields are at least 40px tall.
- The viewport is `viewport-fit=cover`; the toolbar, bottom bar and dialogs avoid the notch and the home indicator (`safe-area-inset`).
- "Add to Home Screen" (PWA manifest, in the release build). With HTTPS, it could later register as a target of the system share sheet.

Touch devices (any width, `pointer: coarse`): icon buttons are 44px, capsules and tabs taller; there is no hover, so row checkboxes and actions are always shown. The file field for `.torrent` files has no `accept` filter, because iOS does not know the `.torrent` type and would gray out every file; the extension is checked after choosing.

### Add flow

1. The type is detected as soon as something is pasted: `http(s)://`, `ftp://`, `magnet:?`, `.torrent` URLs; several lines are several items. When a block of text is pasted, every link in it is found ("Found 3 links in the text"). A URL ending in `/` or `.html` is treated as a web page, and adding it lists the download links on the page. File-hosting links get a hint such as "Downloads after signing in with your 1fichier account" or "MEGA is not supported yet". The icon on the left switches with the type and plays its entrance once (URL: chain links snap together; magnet: a magnet pulls in particles; torrent: a page corner folds open).
2. "Add" or Enter opens a **dialog**; folder choice is not on the main screen:
   - URLs (one or more), links found in text, links on a web page: a checklist with quick filters by extension such as "Select all", "Only .iso", and "Select none"; each row is labelled with its type (URL, torrent, magnet, web page, a file-hosting account), and web pages themselves are unchecked by default. Choose "Temporary location", "Move to when finished" and "Site account", then "Start download".
   - **Duplicate detection**: items already in the list (same URL, same name, or a file of the same name and size already at the destination) are marked "Already in the list" and unchecked by default.
   - **The same torrent** (another magnet link or .torrent with the same infohash): no add dialog; instead "This torrent is already in the list", where "Merge into existing task" only adds the new trackers. Torrents with a different infohash but the same content are listed under "Sources (merged)" in the details panel.
   - **Locations and accounts** (as in the official "New task"):
     - **Temporary location**: where files stay while downloading. URL tasks actually go into `@DownloadCenterTemp/<task>/` in that shared folder; torrents download and seed right there.
     - **Move to when finished**: the first option is "Do not move (stay in the temporary location)". For torrents the label is "Move when seeding ends to", and the move happens only when seeding ends.
     - **Site account** (only with URL tasks; not for torrents or file-hosting links): "Auto (look up a saved account by site)" by default, "None", the saved accounts (site and account name), "Enter manually…". Entering manually reveals user name and password fields below, used only for this task and not saved to the account list.
   - Every dialog has "When finished": keep in the list, remove when the download finishes, remove when seeding finishes (torrents only). Only the list entry is removed; files stay.
   - Torrents / magnets: the file list (Select all, Only videos, Select none), the selected size and the free space of the folder, plus the same temporary location and move-to choices. A magnet link must fetch its file list first, and "Start download" is disabled meanwhile.
   - "Temporary location" defaults to the folder used last, then to the default in settings; "Move to when finished" defaults to the default in settings.
   - **Regular users** have no folders to choose: the dialog just says "Saved to home/Download" and has no "Move to when finished"; site accounts list only their own saved accounts.
3. Dropping a `.torrent` anywhere on the page shows a full-page drop hint and, on release, the same dialog. Phones have no drag and drop: tap "+", then "Choose .torrent file", then the same dialog.
4. On success the new task drops into the list from the top as its first row. This is the list's only entrance animation.

### Deleting

Confirmed in an in-page dialog, never `confirm()`. The option "Also delete downloaded files" is unchecked by default. A delete can be undone for 6 seconds, except when files were deleted too. For unfinished URL tasks the temporary files are always removed, whatever that option says.

### Where files go

- URL downloads in progress live in `@DownloadCenterTemp/<task>/` of the same shared folder and are moved to "Move to when finished" (or to the temporary location when nothing is set) only when complete. Half-downloaded URL files never show up in the shared folder. The details panel's Overview shows the "Temporary location" and the location after finishing.
- Torrents download straight into the "Temporary location" and seed there, so their files are visible while downloading; they move to "Move when seeding ends to" only when seeding ends.
- The help text of Settings › Download › Folders spells out both rules; regular users see the version about their own home folder.

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
| Download | Default temporary location and move-to folder, finished tasks (default for removing them automatically), concurrent downloads, speed limits and the values for limited hours, torrents (torrent engine, ports, UPnP (libtorrent only), DHT/LSD/PEX, seeding conditions, incoming port test), proxy servers (HTTP; SOCKS5 as well when the torrent engine is libtorrent), sign-in (an explanation of the QTS rules, direct sign-in from the QTS desktop) | Only "Files are saved to home/Download in your home folder, owned by you" |
| Users | Who can use Download Center, each as administrator or regular user. Picked from QTS accounts; only members of the QTS administrators group can be administrators, and for everyone else the role menu is locked to regular user with the reason shown. The QTS administrator is added automatically at install. A reminder appears when the home folder service is off | — |
| Schedule | 7×24 drag-to-paint: full speed, limited, paused; clicking a weekday paints the whole day, clicking an hour paints it across the week; a plain-language summary below | — |
| Site accounts | Logins (HTTP/FTP sites) and file-hosting accounts (1fichier, Rapidgator, Real-Debrid, AllDebrid, cookies for other sites). Verified file-hosting accounts show their plan, expiry date and traffic left today, and can be verified again; MEGA and free downloads that need a captcha are not supported | Their own |
| Access tokens | See below | Their own |
| Notifications and integration | See below | Their own |
| Import from the official package | What was detected and checkable items (settings, unfinished tasks, finished history, site accounts), then stopping the official package, importing and verifying. Shown only when official data is detected | — |

Features the current engine cannot do get no switch and no "not supported" text. A one-line instruction appears only when users must do something themselves (for example, without UPnP in the engine, the incoming port help says "Forward these ports on your router to the NAS"). Options follow the engine's capabilities, never a hard-coded engine name: proxy types are "None / HTTP / SOCKS5", and SOCKS5 adds "Resolve domain names through the proxy" and "Torrent connections". "Resolve domain names through the proxy" and "Proxy only" are on by default. Since aria2 was removed on 2026-10-02 there is no torrent engine choice any more; when this NAS cannot download torrents or FTP, one line under the matching concurrent-downloads field says why.

"Test incoming ports" requires checking "Allow an external service to test incoming ports" first (off by default, because it tells an outside service the NAS's public IP and ports); the result says in one sentence which ports are reachable and which need forwarding on the router.

### Access tokens

- List: name, `dct_<id>_…`, permission tags ("Remove tasks and delete files" in vermilion), expiry date, last use time and IP; can be regenerated or revoked. Revoking asks for confirmation.
- Create: name → permission preset (read only / add downloads / full control (without deleting files) / custom, which expands to per-permission checkboxes) → which tasks it may act on (mine / everyone's, the latter for administrators only) → allowed folders → allowed IPs → expiry.
- After creation the full token is shown once, with a "Copy" button. Once closed, the full token cannot be seen again.
- "For developers" at the bottom: API address, authentication, where the documentation is.

### Notifications and integration

Notifications and chat control are one bot channel, not two features.

- **Channel list**: service and name, which events it reports, quiet hours and how messages are batched. Each row has a "Control downloads in the channel" switch:
  - Telegram: can be turned on.
  - LINE: can be turned on when the NAS is reachable from outside over HTTPS.
  - Discord, Slack, ntfy and the like: the switch is disabled with "This service can only receive notifications".
  - Webhook: notes that your own service calls `/api/v1/commands`.
  - When on, it shows "N users linked" and "Link my account", which generates a channel-specific `/link 123456`, valid for 10 minutes.
- **Add channel**: pick the service, fill in its fields, the events to report, "Control downloads in the channel", scope (administrators can choose everyone's tasks), quiet hours, batching. A channel with control enabled shows its pairing code right after saving.
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

**State icons**: downloading (an arrow drops into a tray), waiting (clock hands sweep), paused (two bars spring apart, then rest), seeding (a sprout sways, ripples spread), finished (a checkmark draws itself), checking (a magnifier scans), moving (an arrow slides into a folder), error (an exclamation mark shakes once).

**Action icons**: add (turns 90°), URL (chain links snap together), magnet (the magnet swings and pulls particles in), torrent file (a corner folds open), start (pushes right), pause (squashes and springs back), delete (the bin lid lifts), move up / move down (bounce), folder (opens), schedule (a calendar page flips), speed (the needle swings), site accounts (a key turns), settings (the gear turns), choose files (checks in sequence), retry (one full turn), details (dots hop), import (an arrow drops), access tokens (a ticket tilts), notifications (a bell rings), chat commands (typing dots hop), webhook (a plug goes in), copy (sheets shift apart), sign in (a lock opens).

**Reduced motion**: with `prefers-reduced-motion: reduce` all animation stops; states stay distinguishable by shape and color.

## Copy principles

- Verbs say what will happen: "Add", "Start download", "Pause", "Delete", "Open folder". The button is "Delete", so the toast says "Deleted 2 tasks".
- Errors give the cause and the next step: "FTP sign-in failed (530). Check the login for backup.lan in Settings › Site accounts."
- Empty states are invitations: "No downloads yet. Paste a URL above, or drag a .torrent file into this window."
- Say "torrent" and "URL" to users, not "BT" or "HTTP"; terms such as DHT and PEX appear only in the torrent settings, each with a plain-language line.

## Accessibility

- Keyboard: `/` focuses the input, Tab to a row then `Enter` opens details, Space checks it, `Esc` closes a dialog or the drawer.
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
