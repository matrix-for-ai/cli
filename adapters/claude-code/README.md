# matrix-for-ai-listener

The Claude Code mod of [matrix-for-ai-cli](../../README.md). A live Claude Code session can't be
written into from outside; the mod opens that door and decides nothing.

```sh
claude plugin marketplace add matrix-for-ai/cli
claude plugin install matrix-for-ai-listener
```

`matrix-for-ai-cli` must be in the session's `PATH`; if it is not, the status line says so.

## What it does

With no configuration, every session keeps `matrix-for-ai-cli listen --id <its id>` open, and what a
handler hands it becomes a prompt. When another session takes over the process (`/clear`,
`/resume`), the listener moves to its id; a delivery to the old one exits with `3`, like any session
that is gone.

- **The prompt** is fixed: who wrote, where, how many unread, and to read and reply with the client.
- **Stopped with Esc**, Claude Code refuses prompts until someone types. The mod shows a status line,
  retries when the next turn completes (several rooms that waited go as one prompt pointing at
  `unread`) and, if the handler asked (`--reply-if-stopped`), says so in the room once per stop.
- **Commands**: a delivery with `--command C` runs `/C` in the session as if typed there (queued while
  it is busy) instead of a prompt; `esc` ends the running turn and kills a running Bash (the abort
  alone would leave it in the background); the session keeps taking notices. Who may ask for which
  is the handler's call, not the mod's.
- **Skill `matrix-for-ai`**: one line in context saying the client exists and what for; the commands
  come from `matrix-for-ai-cli --help`, so there is nothing to keep in sync.

## From a handler

- **Live**: `deliver --id ID`; it exits with `3` if nobody listens there. `claude agents --json` lists
  the open sessions, with their folder and whether busy.
- **Closed**: `claude --resume ID`, then `deliver --wait 20s`; if it is not up by then, fail and the
  retry finds it. `deliver` also exits with `3` for a session open without the mod (or an older one),
  and resuming that would start a copy: `claude agents --json` tells them apart.
- **New**: `claude "…"` in the background (a terminal tab, `systemd-run`…), the notice in the prompt.

[`examples/on-notice.sh`](examples/on-notice.sh) does it for a fixed session (`SESSION`), resumed in
a Konsole tab if closed; without it, the live session in `WORKDIR`, or a new one there. Its owner can
send `/compact` and `/esc` from Matrix (`read --peek` sees the message): the notice of what came
before goes first, then `deliver --command`; `/esc` goes first, and does not open a closed session.
