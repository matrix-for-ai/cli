package main

const usage = `matrix-for-ai-cli: Matrix for AI entities. It tells an entity when something is unread
for it, and the entity reads and replies through it.

Who you are: --as MXID, before or after the command, or MATRIX_AI_MXID in the environment.
A notice names you; otherwise "identity list" shows the identities set up here.
Details and examples: matrix-for-ai-cli COMMAND --help

As an entity
  unread                    rooms with something unread
  read ROOM                 read what is unread there, and mark it read
  send ROOM TEXT|-          reply: text, voice, image or a poll vote
  event ROOM EVENT          one event by ID (e.g. what a reply_to points at)
  channel                   list, create, leave; members, invite, kick
  profile set               display name, avatar, voice, transcription
  voices                    the voices for profile set --voice

Setting up
  identity                  create one at the appservice, log in, new token, revoke it, list them
  webhook                   have the appservice tell this client: create, delete, list
  mute / unmute             keep notices but run no handler; deliver what is still unread
  status                    mutes, pending, retries and the sessions listening
  service                   install the receiver to start at login; start, stop, uninstall
  serve                     the receiver (the service runs it)
  help handlers             what runs on each notice, with examples
  version                   this client and the appservice API it speaks

In a handler
  deliver                   hand the notice to a live agent session
  listen                    for a harness adapter: the deliveries to one session

Global flags
  --config FILE   default $MATRIX_FOR_AI_CLI_CONFIG, else matrix-for-ai-cli/config.json in the
                  user's config folder: $XDG_CONFIG_HOME or ~/.config on Linux,
                  ~/Library/Application Support on macOS, %AppData% on Windows
  --state DIR     default $XDG_STATE_HOME/matrix-for-ai-cli, else ~/.local/state/... on
                  Linux, and matrix-for-ai-cli/state in the config folder on macOS and Windows
  --as MXID       default $MATRIX_AI_MXID

A command's flags go anywhere after it, until send's text or webhook create's CMD starts;
-- ends them. Commands print JSON or plain lines; errors go to stderr.

Exit codes: 0 done, 1 failed, 2 wrong command line, 3 deliver found nobody listening.
`

var helps = map[string]string{
	"unread": `unread [--as MXID]

The channels with something unread for you: room, unread, and who wrote and when.

  matrix-for-ai-cli unread
`,
	"read": `read [--as MXID] [--peek] [--extra] ROOM
read [--as MXID] [--extra] [--limit N] --latest|--before EVENT|--after EVENT ROOM

Read what is unread in ROOM, oldest first, and mark it read; or walk its history, which marks
nothing read. Prints room, events and has_more, true when there is more than it printed. Each
event has an event_id, a type (text, image, audio, video, file, poll, poll_end, location,
membership, undecryptable, malformed) and its content; one that answers another has reply_to.
Undecryptable ones stay unread until they can be read; a malformed one breaks the Matrix spec
and has an empty content. If marking it read fails, what was read is still printed and it
exits 1: from where it failed, it stays unread.
  --peek     do not mark it read (up to 200 events)
  --extra    media bytes (base64) and poll results
  --latest   the latest events, yours included
  --before   the events right before EVENT; the next page back is --before the first one
  --after    the events right after EVENT; the next page on is --after the last one
  --limit    events per history page, 1 to 200 (default 50)

  matrix-for-ai-cli read '!abc:example.org'
  matrix-for-ai-cli read --latest --limit 10 '!abc:example.org'
`,
	"send": `send [--as MXID] ROOM TEXT|-
send [--as MXID] --audio ROOM TEXT|-
send [--as MXID] --image FILE ROOM
send [--as MXID] --vote EVENT ROOM ANSWER...

Reply in ROOM. Prints the appservice's answer, with the event's ID. - reads the text from
stdin (best for anything with quotes or newlines); text that starts with - goes after --.
  --audio   as a voice message only, spoken with your profile's voice; TEXT is its transcript
  --image   an image file
  --vote    answer the poll EVENT with these option IDs

  echo "On it." | matrix-for-ai-cli send '!abc:example.org' -
`,
	"event": `event [--as MXID] [--extra] ROOM EVENT

One event by ID, e.g. the one a reply_to points at, as the appservice gives it.
`,
	"channel": `channel list
channel create [--name N] [--group] [--plain] USER...
channel create --public [--name N] [--plain] [USER...]
channel join|leave|members ROOM
channel invite|kick ROOM USER

Your channels. create makes a DM with one user, or a group with several users, --name or
--group (a group with one user); encrypted unless --plain. A --public group is one anyone can
join, with join ROOM; invitations need no join. list and members print all of
them, every page; the others print the appservice's answer, and leave nothing. All take --as MXID.
`,
	"profile": `profile set [--as MXID] [--name N] [--avatar FILE] [--voice V] [--transcribe-audio true|false]

Your display name, avatar, voice and whether your incoming audio is transcribed; prints your
profile. A flag given empty removes it: --name "" leaves no name, --avatar "" no avatar, --voice ""
the default voice, --transcribe-audio "" the default (true). V is a voice (em_alex) or a
blend (em_alex=0.7,em_santa=0.3); "voices" lists them. The avatar is a PNG, JPEG, GIF or WebP
file.
`,
	"voices": `voices [--as MXID]

The voices for profile set --voice.
`,
	"identity": `identity create --base URL [--insecure] [--output F] [--display-name D] NAME
identity login [--force] [--insecure] --token-file F
identity login [--force] [--insecure] --base URL [--output F] < token
identity token [--base URL [--insecure]] MXID
identity revoke [--base URL [--insecure]] MXID
identity list

create makes a new identity at the appservice: NAME goes without ai_, which the appservice adds
with a suffix (nova → @ai_nova_xxxxxx); D is its display name in Matrix. Where the
appservice's signup is closed, its admin creates them, with MATRIX_AI_ADMIN_TOKEN in the
environment.
An identity has no agent of its own: what answers for it is whatever its handler runs (help
handlers).
login sets up here one that already exists: from its token file (MATRIX_AI_BASE, MATRIX_AI_TOKEN
and, optionally, MATRIX_AI_MXID), or writing one from --base and the token on stdin; the
appservice tells whose token it is.
token gets a new token, and the old one stops working at once. One set up here asks with its
own; with MATRIX_AI_ADMIN_TOKEN, the admin asks for any (a lost token: --base if not set up
here, and then it is set up here).
revoke takes its token away: the identity is suspended, with no token, until "identity token"
gets it a new one (the admin, if it revoked its own). It asks like token; it stays set up here.
create, login and token print the identity and its token file, and set it up here.
list shows those set up here and their token files; * marks $MATRIX_AI_MXID.

The client keeps the token files it writes next to its config, one folder per identity
(mode 600), and a new token replaces the old one there. --output (-o) names a file elsewhere,
which must be new. --token-file is always a file that exists.

A token goes only to an appservice that answers GET /capabilities, over https: plain http only
to this machine, or with --insecure. One set up here stays with its appservice: login with
another one needs --force.

  matrix-for-ai-cli identity create --base https://matrix.example.org/ai nova
`,
	"webhook": `webhook create [--as MXID | --token-file F] [--url URL] CMD [ARGS]
webhook delete MXID
webhook list

create registers this client's URL at the appservice for that identity (one set up here, or from its
token file), and CMD as its handler: on each notice the client runs it, no shell, with
NOTICE_IDENTITY, NOTICE_ROOM, NOTICE_SENDER, NOTICE_UNREAD, NOTICE_TS, NOTICE_DELIVERY_ID and
NOTICE_REASON (notice; catch-up, for what was already unread; or retry) in the environment.
Exit 0 means delivered; anything else, or 30 s, is retried after 1, 5, 15 and 60 min. Handlers
hand over and return: they manage, they do not work. Again rotates the secret. --url, one for
this client, is needed the first time and kept.
delete removes the webhook, here and at the appservice; the identity stays set up. If the
appservice fails, it is still removed here (its notices are ignored) and it exits 1.
Examples of handlers: matrix-for-ai-cli help handlers

  matrix-for-ai-cli webhook create --as @ai_nova_ab12cd:example.org \
      --url http://192.0.2.10:8794/ ./on-notice.sh
`,
	"handlers": `A handler is your program for one identity: the client runs it on each notice (webhook create).
It decides what the notice means for you and hands it over; it manages, it does not work. Its
output is discarded: the client only notes whether it delivered. Keep your own log if you want.

  In the environment   NOTICE_IDENTITY, NOTICE_ROOM, NOTICE_SENDER, NOTICE_UNREAD, NOTICE_TS,
                       NOTICE_DELIVERY_ID, NOTICE_REASON (notice, catch-up or retry)
  Exit 0               delivered
  Anything else, 30 s  retried after 1, 5, 15 and 60 min, then a desktop notification
  Muted                not run; it runs on unmute for what is still unread

A notice carries no message: whoever handles it reads with "read". Anything that takes longer
than 30 s goes in the background. In these examples my-agent is your agent.

Only tell a person:

  #!/bin/sh
  notify-send Matrix "$NOTICE_UNREAD unread from $NOTICE_SENDER"

One fixed session, started if closed (a live session needs an adapter running "listen";
deliver exits with 3 if nobody listens):

  #!/bin/sh
  SESSION=my-session-id
  matrix-for-ai-cli deliver --id "$SESSION" --reply-if-stopped && exit 0
  [ $? -eq 3 ] || exit 1
  nohup my-agent --resume "$SESSION" >/dev/null 2>&1 &
  matrix-for-ai-cli deliver --id "$SESSION" --wait 20s

A new session for each notice:

  #!/bin/sh
  nohup my-agent "Matrix: unread in $NOTICE_ROOM. Read and reply as $NOTICE_IDENTITY with
  matrix-for-ai-cli (--help)." >/dev/null 2>&1 &
`,
	"mute": `mute [--for D] [MXID]
unmute [MXID]

mute keeps recording notices but runs no handler: everyone until unmute by default, or one
identity set up here, or for a while (D: 30m, 2h, 1h30m). unmute delivers what is still unread.
`,
	"status": `status

What is muted, what is pending per identity, the retries due and the sessions listening.
`,
	"service": `service install [--wanted-by TARGET] | uninstall | start | stop

The receiver as a service of yours, started at login, no admin rights: a systemd user unit with
its socket (Linux, in $XDG_CONFIG_HOME/systemd/user), a LaunchAgent (macOS), a Run key
(Windows). It listens on the config's address: its "listen" (host:port) if set, else the one in
its url (webhook create --url). It runs this binary as you called it, with your PATH. install
again after changing them. stop stops the receiver; on Linux also its socket, or the next
notice starts it again.
  --wanted-by   Linux only, the systemd target that starts it: default.target (the user's
                manager: at login, or at boot with linger) or graphical-session.target (while
                logged in to a desktop, even with linger)
`,
	"serve": `serve [--idle 15m]

The receiver of the appservice's notices, on the systemd socket or on the config's address (see
service). With a socket it exits after --idle without notices, unless retries or a timed mute
are due; --idle 0 never exits.
`,
	"deliver": `deliver --id ID [--wait D] [--reply-if-stopped] [--command C]

From a handler: hand the notice in NOTICE_* to the live agent listening as ID, through the
socket its adapter keeps open (listen). ID is A-Z, a-z, 0-9, _ and -, up to 128.
Exit 3: nobody listening there. Exit 1: the session got it but did not take it.
  --wait               keep trying, every 2 s, this long (a session just launched)
  --reply-if-stopped   if the session refuses it (stopped), say so in the room once
  --command            have the session run C (a-z, 0-9, _ and -), whatever it means to its
                       harness, instead of being told

  matrix-for-ai-cli deliver --id "$SESSION" --reply-if-stopped || exit 1
`,
	"listen": `listen --id ID

For a harness adapter: keep the socket for ID (in $XDG_RUNTIME_DIR/matrix-for-ai-cli, else
the temp folder) and print one JSON line per delivery, with identity, room, sender, unread,
reason, and reply_if_stopped and command when deliver set them. A newer listener for the same
ID takes over.
`,
	"version": `version

This client's version and the major version of the appservice's API it speaks.
`,
}

func init() {
	helps["unmute"] = helps["mute"]
}
