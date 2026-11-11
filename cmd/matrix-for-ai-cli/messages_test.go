package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// mailbox plays the appservice's unread list: pages of two, whatever the limit, and undecryptable
// messages that stay unread after any receipt but show only once.
type mailbox struct {
	mu          sync.Mutex
	unread      []string
	shown       map[string]bool
	failReceipt bool
	receipts    []string
}

func (m *mailbox) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasSuffix(r.URL.Path, "/receipt") {
		if m.failReceipt {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(502)
			w.Write([]byte(`{"type":"about:blank","title":"Bad Gateway","status":502,"code":"homeserver_error","detail":"the homeserver failed"}`))
			return
		}
		var body struct {
			EventID string `json:"event_id"`
		}
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &body)
		m.receipts = append(m.receipts, body.EventID)
		at := slices.Index(m.unread, body.EventID)
		var kept []string
		for _, id := range m.unread[:at+1] {
			if strings.HasPrefix(id, "$u") {
				kept = append(kept, id)
			}
		}
		m.unread = append(kept, m.unread[at+1:]...)
		w.WriteHeader(204)
		return
	}
	if m.shown == nil {
		m.shown = map[string]bool{}
	}
	var events []map[string]any
	left := 0
	for i, id := range m.unread {
		if len(events) == 2 {
			left = len(m.unread) - i
			break
		}
		if strings.HasPrefix(id, "$u") && m.shown[id] {
			continue
		}
		m.shown[id] = true
		event := map[string]any{"event_id": id, "room": "!r:example.org", "sender": "@alice:example.org", "ts": 1, "type": "text", "content": map[string]any{"text": "hi"}}
		if strings.HasPrefix(id, "$u") {
			event["type"], event["content"] = "undecryptable", map[string]any{}
		}
		events = append(events, event)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"events": events, "has_more": left > 0})
}

func readUnread(t *testing.T, m *mailbox, args ...string) (ids []string, hasMore bool, err error) {
	appservice := contractAppservice(t, http.HandlerFunc(m.serve))
	defer appservice.Close()
	work := cliWorld(t, appservice.URL)
	for _, kv := range (cliCase{}).environ(work, appservice.URL) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err = cli(append([]string{"read"}, append(args, "!r:example.org")...))
	os.Stdout = stdout
	w.Close()
	var out struct {
		Events []struct {
			EventID string `json:"event_id"`
		} `json:"events"`
		HasMore bool `json:"has_more"`
	}
	json.NewDecoder(r).Decode(&out)
	for _, msg := range out.Events {
		ids = append(ids, msg.EventID)
	}
	return ids, out.HasMore, err
}

func TestReadUnreadPages(t *testing.T) {
	m := &mailbox{unread: []string{"$u1", "$a", "$b", "$c"}}
	ids, more, err := readUnread(t, m)
	if err != nil || more || !slices.Equal(ids, []string{"$u1", "$a", "$b", "$c"}) {
		t.Errorf("got %v, has_more %v, %v", ids, more, err)
	}
	if !slices.Equal(m.unread, []string{"$u1"}) || !slices.Equal(m.receipts, []string{"$a", "$c"}) {
		t.Errorf("left unread %v after receipts %v", m.unread, m.receipts)
	}
}

func TestReadPastUndecryptable(t *testing.T) {
	m := &mailbox{unread: []string{"$u1", "$u2", "$a"}}
	ids, more, err := readUnread(t, m)
	if err != nil || more || !slices.Equal(ids, []string{"$u1", "$u2", "$a"}) {
		t.Errorf("got %v, has_more %v, %v", ids, more, err)
	}
}

func TestReadPeekMarksNothing(t *testing.T) {
	m := &mailbox{unread: []string{"$a", "$b", "$c"}}
	ids, more, err := readUnread(t, m, "--peek")
	if err != nil || !more || !slices.Equal(ids, []string{"$a", "$b"}) || len(m.receipts) > 0 {
		t.Errorf("got %v, has_more %v, receipts %v, %v", ids, more, m.receipts, err)
	}
}

func TestReadReceiptFailsMidway(t *testing.T) {
	m := &mailbox{unread: []string{"$a", "$b", "$c"}, failReceipt: true}
	ids, more, err := readUnread(t, m)
	if !errors.Is(err, errPartial) || !more || !slices.Equal(ids, []string{"$a", "$b"}) {
		t.Errorf("got %v, has_more %v, %v", ids, more, err)
	}
}
