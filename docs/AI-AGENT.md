# Using Download Center from an AI agent

AI coding agents such as Claude Code, Codex and Gemini CLI can operate Download Center for you: "download this link to the NAS", "how far along is the Debian ISO?", "pause everything until tonight", "which downloads failed and why?". They do it through the REST API ([INTEGRATION.md](INTEGRATION.md)) with an access token of their own, guided by a skill file that tells them how the API works and what they must not do.

The agent runs on your computer and talks to the NAS directly. It needs to reach the address you use for Download Center in the browser, so agents that run in the cloud (chat websites) cannot reach a NAS that is only on your local network.

## Set it up

1. In Download Center, open **Settings › Access tokens** and click **Create token** in the **AI Agent** section. The window is filled in for an agent: the name "AI Agent" and the "Full control (no file deletion)" permissions. Adjust the expiry or restrict it to your computer's address if you like, then create it.
2. The next window shows the token once, and under **For AI agents** a one-line setup command. Pick your agent, copy the command and run it in a terminal on the computer where the agent runs. It
   - downloads the skill file from your NAS into the agent's skills folder, and
   - saves the NAS address and the token to `~/.config/download-center/config`, readable only by you.
3. Start a new session of the agent and ask it something about your downloads.

On Windows, run the command in Git Bash or WSL, or install by hand (below).

## Where the skill goes

| Agent | Skill file |
|---|---|
| Claude Code | `~/.claude/skills/download-center/SKILL.md` (or `.claude/skills/download-center/SKILL.md` in a project) |
| Codex, Gemini CLI and other agents that read `~/.agents/skills` | `~/.agents/skills/download-center/SKILL.md` |
| Agents without skill support | Paste the contents of `SKILL.md` into the agent's instructions file (for example `AGENTS.md`) |

The skill file is served by your NAS at `<Download Center address>/docs/skill/SKILL.md`, for example `https://nas.example.com:8081/DownloadCenter/docs/skill/SKILL.md`, and can also be downloaded from the AI Agent section (**Install or update the skill by hand**).

## The connection file

`~/.config/download-center/config` holds two lines:

```sh
DC_URL=https://nas.example.com:8081/DownloadCenter
DC_TOKEN=dct_xxxxxxxx_…
```

`DC_URL` is the address of the Download Center page without the trailing slash. If you prefer, set `DC_URL` and `DC_TOKEN` as environment variables of the agent instead; the skill uses them when they are set.

To create the file by hand:

```sh
mkdir -p ~/.config/download-center
(umask 077; printf 'DC_URL=%s\nDC_TOKEN=%s\n' 'https://nas.example.com:8081/DownloadCenter' 'dct_…' > ~/.config/download-center/config)
```

## Keeping it current

The skill describes the API of the Download Center version it came from. After updating Download Center, run the command under **Install or update the skill by hand** again; it replaces the skill file and leaves the connection file alone.

When the token expires, or after you regenerate it, the agent gets `token_invalid` or `token_expired`. Regenerate the token (or create a new one) and run the setup command shown with it.

## Security

- The token is a password for your downloads. It never grants more than your own account, and it stops working when your account loses access to Download Center in QTS.
- Give the agent a token of its own, so that you can see its requests (last use, address, count) and revoke it without affecting anything else.
- The default permissions do not include **Remove tasks and delete files**. Add it only if you want the agent to delete data; the skill asks the agent to confirm with you before deleting anything.
- Restrict the token to your computer's address under **Allowed source IPs** when that address is fixed.
- Do not paste the token into the chat: everything you type goes to the agent's model provider. The setup command keeps it in a file the agent reads only through the shell.
- The setup command contains the token, so your shell may save it in its history (`~/.bash_history`, `~/.zsh_history`). Start the command with a space (bash with `HISTCONTROL=ignorespace`, zsh with `HIST_IGNORE_SPACE`), or delete the entry afterwards (`history -d`).
- What the agent reads from the API (task names, file names, logs) does reach its model provider. The names of torrents and files come from the internet and may contain text written to mislead an AI; the skill tells the agent to treat them as data and never as instructions, but review what it proposes before agreeing to removals.
- Use HTTPS when the NAS is reachable from outside your network.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `curl: (60) SSL certificate problem` | The NAS uses a self-signed certificate. Install a valid certificate in QTS (Let's Encrypt), or use the NAS's local `http://` address on your own network |
| `401 token_invalid` / `token_expired` | The token was revoked, regenerated or has expired |
| `403 insufficient_scope` | The token lacks a permission; edit it in **Settings › Access tokens** |
| `403 ip_not_allowed` | The token is restricted to other addresses |
| The agent does not use the skill | Start a new session, or ask it to "use the download-center skill" |
| `curl: command not found` | Install curl, or use Git Bash on Windows |
