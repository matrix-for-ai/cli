#!/bin/sh
# A handler for the Claude Code session of one folder, never two. It manages, it doesn't work: hand
# over and return (the client gives it 30 s). Exit 0 = delivered; anything else, the client retries.
set -eu
# Set by whoever calls it: OWNER, the only one who may send commands, and only these; WORKDIR, where
# the session runs; SESSION, optional, a fixed session's ID, resumed if closed. Without SESSION the
# notice goes to the live session in WORKDIR (the newest, if there are several), or to a new one.
: "${OWNER:?}" "${WORKDIR:?}"
COMMANDS='compact|esc'
export PATH="$HOME/.local/bin:$PATH"

fixed=${SESSION:-}
live() {
  claude agents --json | jq -r --arg d "$WORKDIR" \
    '[.[] | select(.cwd == $d and .kind == "interactive")] | sort_by(.startedAt) | last | .sessionId // empty'
}
SESSION=${fixed:-$(live)}

deliver() { matrix-for-ai-cli deliver --id "$SESSION" --reply-if-stopped "$@"; }
open() { claude agents --json | jq -e --arg s "$SESSION" 'any(.[]; .sessionId == $s)' >/dev/null; }

# A command from the owner, the whole message and the last one: the session runs it, after it is told
# of what came before (in order). Alone, it is marked read and that is all.
cmd=
if [ "$NOTICE_SENDER" = "$OWNER" ]; then
  unread=$(matrix-for-ai-cli read --peek --as "$NOTICE_IDENTITY" "$NOTICE_ROOM")
  cmd=$(printf '%s' "$unread" | jq -r --arg o "$OWNER" --arg c "^/($COMMANDS)$" \
    '.events | last | select(.sender == $o and .type == "text") | .content.text | gsub("^\\s+|\\s+$"; "")
     | select(test($c)) | ltrimstr("/")')
fi

# reach hands it over to the session, opening it if there is none: resumed if fixed, else a new one,
# in a Konsole tab (visible, not --bg), by systemd-run so the tab outlives the client. If it is not up
# in 20 s, the retry finds it.
reach() {
  if [ -n "$SESSION" ]; then
    deliver "$@" && return
    open && { echo "session open but not listening (an older mod?): retry later"; return 1; }
  fi
  if [ -n "$fixed" ]; then
    start="--resume $SESSION"
  else
    SESSION=$(uuidgen)
    start="--session-id $SESSION"
  fi
  systemd-run --user --quiet --collect --setenv=PATH="$PATH" konsole --new-tab --workdir "$WORKDIR" \
    -e bash -c "claude $start; exec bash"
  deliver --wait 20s "$@"
}

alone() { [ "$(printf '%s' "$unread" | jq '.events | length')" -eq 1 ]; }
seen() { matrix-for-ai-cli read --as "$NOTICE_IDENTITY" "$NOTICE_ROOM" >/dev/null; }

# esc is the key, not a turn: it goes first, and a closed session has nothing to stop.
if [ "$cmd" = esc ]; then
  cmd=
  { [ -n "$SESSION" ] && deliver --command esc; } || echo "esc: nothing to stop"
  alone && { seen; exit 0; }
fi
# Alone, a command goes to a fixed session, resumed if closed, or to the live one: a new session would
# have nothing to run it on.
if [ -n "$cmd" ] && alone; then
  [ -z "$SESSION" ] || reach --command "$cmd"
  seen
  exit 0
fi
reach
[ -z "$cmd" ] || deliver --command "$cmd" || echo "/$cmd: not taken (the notice was)"
