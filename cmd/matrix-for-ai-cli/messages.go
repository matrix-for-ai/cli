package main

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func entityFor(cfg *Config, as string) (mxid, base, token string, err error) {
	if as == "" {
		return "", "", "", usageError("", "--as MXID is required (or MATRIX_AI_MXID in the environment)")
	}
	e, ok := cfg.Entities[as]
	if !ok {
		return "", "", "", notHere(cfg, as)
	}
	base, token, mxid, err = appserviceCredentials(e)
	if err == nil && mxid == "" {
		mxid = as
	}
	if err == nil && mxid != as {
		err = fail(errTokenFile, "%s: its token file says %s", as, mxid)
	}
	return mxid, base, token, err
}

func asFlag(fs *flag.FlagSet) *string {
	return fs.String("as", os.Getenv("MATRIX_AI_MXID"), "")
}

func cmdUnread(configPath, state string, args []string) error {
	fs := flag.NewFlagSet("unread", flag.ExitOnError)
	as := asFlag(fs)
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	mxid, base, token, err := entityFor(cfg, *as)
	if err != nil {
		return err
	}
	channels, err := appservicePages(base+"/channels?unread=true", token, "channels")
	if err != nil {
		return err
	}
	unread := map[string]bool{}
	for _, c := range channels {
		var r RoomUnread
		if json.Unmarshal(c, &r) == nil && r.Unread > 0 {
			unread[r.Room] = true
		}
	}
	for _, p := range listPending(state)[mxid] {
		if !unread[p.Room] {
			removePending(state, mxid, p.Room)
		}
	}
	return printJSON(map[string]any{"channels": nonNil(channels)})
}

func nonNil(l []json.RawMessage) []json.RawMessage {
	if l == nil {
		return []json.RawMessage{}
	}
	return l
}

func cmdRead(configPath, state string, args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	as := asFlag(fs)
	extra := fs.Bool("extra", false, "")
	peek := fs.Bool("peek", false, "")
	latest := fs.Bool("latest", false, "")
	before := fs.String("before", "", "")
	after := fs.String("after", "", "")
	limit := fs.Int("limit", 50, "")
	parse(fs, args)
	if err := need(fs, "ROOM"); err != nil {
		return err
	}
	limitSet := false
	fs.Visit(func(f *flag.Flag) { limitSet = limitSet || f.Name == "limit" })
	history := 0
	for _, on := range []bool{*latest, *before != "", *after != ""} {
		if on {
			history++
		}
	}
	switch {
	case history > 1:
		return usageError(fs.Name(), "--latest, --before and --after go alone")
	case history == 1 && *peek:
		return usageError(fs.Name(), "--peek is for unread: the history marks nothing read")
	case history == 0 && limitSet:
		return usageError(fs.Name(), "--limit is for --latest, --before and --after")
	case *limit < 1 || *limit > 200:
		return usageError(fs.Name(), "--limit must be 1 to 200")
	}
	room := fs.Arg(0)
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	mxid, base, token, err := entityFor(cfg, *as)
	if err != nil {
		return err
	}
	q := url.Values{}
	if *extra {
		q.Set("include", "media,poll")
	}
	u := roomPath(base, room) + "/events?"
	if history == 1 {
		q.Set("limit", strconv.Itoa(*limit))
		if *before != "" {
			q.Set("before", *before)
		}
		if *after != "" {
			q.Set("after", *after)
		}
		events, hasMore, err := eventsPage(u+q.Encode(), token)
		if err != nil {
			return err
		}
		return printEvents(room, events, hasMore)
	}

	q.Set("unread", "true")
	q.Set("limit", "200")
	u += q.Encode()
	var events []json.RawMessage
	var unmarked string
	var stopped error
	hasMore := true
	for hasMore {
		page, more, err := eventsPage(u, token)
		if err != nil && len(events) == 0 {
			return err
		}
		if err != nil {
			stopped = fail(errPartial, "printed, but there is more unread: %w", err)
			break
		}
		hasMore = more
		events = append(events, page...)
		if len(page) == 0 {
			break
		}
		unmarked = eventID(page[len(page)-1])
		if *peek || !hasMore {
			break
		}
		if err := markRead(base, room, token, unmarked); err != nil {
			stopped = fail(errPartial, "printed, but not marked read: %w", err)
			break
		}
		unmarked = ""
	}
	if err := printEvents(room, events, hasMore); err != nil || stopped != nil || *peek {
		return cmp.Or(err, stopped)
	}
	if unmarked != "" {
		if err := markRead(base, room, token, unmarked); err != nil {
			return fail(errPartial, "printed, but not marked read: %w", err)
		}
	}
	removePending(state, mxid, room)
	return nil
}

func eventsPage(u, token string) (events []json.RawMessage, hasMore bool, err error) {
	var p struct {
		Events  *[]json.RawMessage `json:"events"`
		HasMore bool               `json:"has_more"`
	}
	if err := appserviceCall(http.MethodGet, u, token, nil, &p); err != nil {
		return nil, false, err
	}
	if p.Events == nil {
		return nil, false, fail(errAppserviceAnswer, "the appservice's answer to GET %s has no events", requestPath(u))
	}
	return *p.Events, p.HasMore, nil
}

func eventID(event json.RawMessage) string {
	var m struct {
		EventID string `json:"event_id"`
	}
	json.Unmarshal(event, &m)
	return m.EventID
}

func markRead(base, room, token, eventID string) error {
	body, _ := json.Marshal(map[string]string{"event_id": eventID})
	_, err := appserviceRaw(http.MethodPut, roomPath(base, room)+"/receipt", token, body)
	return err
}

func printEvents(room string, events []json.RawMessage, hasMore bool) error {
	return printJSON(map[string]any{"room": room, "events": nonNil(events), "has_more": hasMore})
}

func cmdSend(configPath string, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	as := asFlag(fs)
	audio := fs.Bool("audio", false, "")
	image := fs.String("image", "", "")
	vote := fs.String("vote", "", "")
	parseUntil(fs, args, 2)
	rest := fs.Args()
	if len(rest) == 0 {
		return usageError("send", "missing ROOM")
	}
	room, rest := rest[0], rest[1:]

	var event map[string]any
	switch {
	case *image != "":
		if len(rest) > 0 {
			return usageError("send", "unexpected %q: --image sends no text", rest[0])
		}
		data, mimetype, err := readUpload("send", "image", *image)
		if err != nil {
			return err
		}
		event = map[string]any{"type": "image", "content": map[string]any{"data_base64": base64.StdEncoding.EncodeToString(data), "mimetype": mimetype,
			"filename": filepath.Base(*image)}}
	case *vote != "":
		if len(rest) == 0 {
			return usageError("send", "missing ANSWER")
		}
		event = map[string]any{"type": "poll_response",
			"content": map[string]any{"poll_event_id": *vote, "answers": rest}}
	default:
		if len(rest) == 0 {
			return usageError("send", "missing TEXT")
		}
		text := strings.Join(rest, " ")
		if text == "-" {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			if text = strings.TrimRight(string(b), "\n"); text == "" {
				return usageError("send", "no text on stdin")
			}
		}
		event = map[string]any{"type": "text", "content": map[string]any{"text": text}}
		if *audio {
			event = map[string]any{"type": "audio", "content": map[string]any{"transcript": text}}
		}
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	_, base, token, err := entityFor(cfg, *as)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(event)
	data, err := appserviceRaw(http.MethodPost, roomPath(base, room)+"/events", token, body)
	if err != nil {
		return err
	}
	return printAppserviceJSON(data)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
