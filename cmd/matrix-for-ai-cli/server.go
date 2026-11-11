package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxSkew = 5 * time.Minute

type Notice struct {
	Identity   string `json:"identity"`
	Room       string `json:"room"`
	Sender     string `json:"sender"`
	Unread     int    `json:"unread"`
	TS         int64  `json:"ts"`
	DeliveryID string `json:"delivery_id"`

	Reason string `json:"-"` // notice (webhook), catch-up or retry
}

type job struct {
	entity Entity
	notice Notice
}

type server struct {
	config func() (*Config, error) // reread on every notice: webhook create needs no restart
	now    func() time.Time
	// Fields so the tests can replace them.
	run   func(name string, e Entity, n Notice) error
	alert func(title, body string)
	fetch func(e Entity) (map[string]RoomUnread, error)
	state string

	mu       sync.Mutex
	queues   map[string]chan job // one queue per entity: never two deliveries at once
	pending  int
	lastSeen time.Time
	caughtUp map[string]bool // unread fetched and handled since start, unmute or a new webhook
}

func newServer(config func() (*Config, error), state string) *server {
	s := &server{config: config, now: time.Now, run: runOnNotice, alert: desktopNotify, fetch: fetchFor, state: state,
		queues: map[string]chan job{}, caughtUp: map[string]bool{}}
	s.lastSeen = s.now()
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.touch()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	cfg, err := s.config()
	if err != nil {
		log.Printf("config: %v", err)
		http.Error(w, "config", http.StatusInternalServerError)
		return
	}
	name, ok := verify(cfg, body, r.Header.Get("X-Signature-256"))
	if !ok {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var n Notice
	if err := json.Unmarshal(body, &n); err != nil || n.Room == "" || n.DeliveryID == "" {
		http.Error(w, "bad notice", http.StatusBadRequest)
		return
	}
	if d := s.now().Sub(time.UnixMilli(n.TS)); d > maxSkew || d < -maxSkew {
		http.Error(w, "stale", http.StatusBadRequest)
		return
	}
	if n.Identity != name {
		log.Printf("notice signed with %s's secret claims to be %s: rejected", name, n.Identity)
		http.Error(w, "identity mismatch", http.StatusUnauthorized)
		return
	}
	if s.firstTime(n.DeliveryID) {
		n.Reason = "notice"
		s.notify(name, *cfg.Entities[name], n)
	}
	w.WriteHeader(http.StatusNoContent)
}

// The secret says who a notice is for, not the identity in its body.
func verify(cfg *Config, body []byte, header string) (string, bool) {
	hexsig, ok := strings.CutPrefix(header, "sha256=")
	got, err := hex.DecodeString(hexsig)
	if !ok || err != nil {
		return "", false
	}
	for name, e := range cfg.Entities {
		if e.Secret == "" {
			continue
		}
		mac := hmac.New(sha256.New, []byte(e.Secret))
		mac.Write(body)
		if hmac.Equal(mac.Sum(nil), got) {
			return name, true
		}
	}
	return "", false
}

func (s *server) firstTime(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	seen := loadSeen(s.state)
	seen.prune(now)
	if _, dup := seen[id]; dup {
		return false
	}
	seen[id] = now.UnixMilli()
	if err := saveSeen(s.state, seen); err != nil {
		log.Printf("delivery ids: %v (a replay after a restart would get through)", err)
	}
	return true
}

func (s *server) enqueue(name string, j job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.queues[name]
	if !ok {
		q = make(chan job, 64)
		s.queues[name] = q
		go s.worker(name, q)
	}
	select {
	case q <- j:
		s.pending++
	default:
		log.Printf("%s: queue full, notice %s dropped (still unread in the appservice)", name, j.notice.DeliveryID)
	}
}

func (s *server) worker(name string, q chan job) {
	for j := range q {
		s.delivered(name, j.notice, s.run(name, j.entity, j.notice))
		s.mu.Lock()
		s.pending--
		s.lastSeen = s.now()
		s.mu.Unlock()
	}
}

func (s *server) touch() {
	s.mu.Lock()
	s.lastSeen = s.now()
	s.mu.Unlock()
}

func (s *server) idle() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending > 0 {
		return 0
	}
	return s.now().Sub(s.lastSeen)
}

func runOnNotice(name string, e Entity, n Notice) error {
	if len(e.OnNotice) == 0 {
		return fmt.Errorf("%s: no on_notice", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.OnNotice[0], e.OnNotice[1:]...)
	cmd.Env = append(os.Environ(),
		"NOTICE_IDENTITY="+n.Identity,
		"NOTICE_ROOM="+n.Room,
		"NOTICE_SENDER="+n.Sender,
		"NOTICE_UNREAD="+strconv.Itoa(n.Unread),
		"NOTICE_TS="+strconv.FormatInt(n.TS, 10),
		"NOTICE_DELIVERY_ID="+n.DeliveryID,
		"NOTICE_REASON="+n.Reason,
	)
	// Its output is its own: not kept, and no pipe for a child left in the background to hold.
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s (handlers hand over and return)", handlerTimeout)
	}
	if err != nil {
		log.Printf("%s: %s notice from %s in %s not delivered: %v", name, n.Reason, n.Sender, n.Room, err)
		return err
	}
	log.Printf("%s: %s notice from %s in %s delivered", name, n.Reason, n.Sender, n.Room)
	return nil
}

func fetchFor(e Entity) (map[string]RoomUnread, error) {
	base, token, _, err := appserviceCredentials(&e)
	if err != nil {
		return nil, err
	}
	return fetchUnread(base, token)
}
