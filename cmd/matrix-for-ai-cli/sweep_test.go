package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a settable now for the server.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// sweepServer: Paul has room !r:hs unread at the appservice (newest event at eventAt), or nothing. fail
// makes the handler fail.
func sweepServer(t *testing.T, unread bool, eventAt time.Time, fail *bool) (*server, chan string, *clock, chan string) {
	c := &clock{t: time.Now()}
	s, _ := testServer(t, c.t)
	s.now = c.now
	got := make(chan string, 8)
	alerts := make(chan string, 8)
	s.run = func(name string, _ Entity, n Notice) error {
		got <- name + " " + n.Room + " " + n.Reason
		if *fail {
			return errors.New("down")
		}
		return nil
	}
	s.alert = func(_, body string) { alerts <- body }
	s.fetch = func(e Entity) (map[string]RoomUnread, error) {
		if e.Secret != "s-paul" || !unread {
			return map[string]RoomUnread{}, nil
		}
		r := RoomUnread{Unread: 1, LastUnread: UnreadEvent{"@alice:hs", eventAt.UnixMilli()}}
		return map[string]RoomUnread{"!r:hs": r}, nil
	}
	return s, got, c, alerts
}

func expect(t *testing.T, got chan string, want string) {
	t.Helper()
	select {
	case g := <-got:
		if g != want {
			t.Fatalf("delivered %q, want %q", g, want)
		}
	case <-time.After(500 * time.Millisecond):
		if want != "" {
			t.Fatalf("nothing delivered, want %q", want)
		}
		return
	}
	if want == "" {
		t.Fatal("delivered, should not have")
	}
}

// settled waits for the worker to record the outcome.
func settled(t *testing.T, s *server, ok func(Pending) bool) Pending {
	t.Helper()
	for i := 0; i < 100; i++ {
		p := s.roomState("@ai_paul:hs", "!r:hs")
		if ok(p) {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pending never settled: %+v", s.roomState("@ai_paul:hs", "!r:hs"))
	return Pending{}
}

func TestDeliveredIsNotToldAgainOnStartup(t *testing.T) {
	fail := false
	s, got, c, _ := sweepServer(t, true, time.Now(), &fail)
	body := notice(c.now(), "d1")
	post(s, body, sign("s-paul", body))
	expect(t, got, "@ai_paul:hs !r:hs notice")
	settled(t, s, func(p Pending) bool { return p.DeliveredAt > 0 && p.NextAt == 0 })
	if s.retrying() {
		t.Fatal("nothing to retry")
	}
	c.add(time.Hour) // the client restarts later; the room is still unread, but nothing new
	s.sweep()
	expect(t, got, "")
}

func TestStartupDeliversWhatIsNewOrUnknown(t *testing.T) {
	fail := false
	now := time.Now()
	s, got, _, _ := sweepServer(t, true, now, &fail) // unknown room
	s.sweep()
	expect(t, got, "@ai_paul:hs !r:hs catch-up")

	s, got, _, _ = sweepServer(t, true, now, &fail) // delivered before the newest event
	s.save("@ai_paul:hs", Pending{Room: "!r:hs", DeliveredAt: now.Add(-time.Hour).UnixMilli()})
	s.sweep()
	expect(t, got, "@ai_paul:hs !r:hs catch-up")
}

func TestFailuresRetryWithBackoffThenAlert(t *testing.T) {
	fail := true
	s, got, c, alerts := sweepServer(t, true, time.Now(), &fail)
	body := notice(c.now(), "d1")
	post(s, body, sign("s-paul", body))
	expect(t, got, "@ai_paul:hs !r:hs notice")
	for i, wait := range retryAfter {
		settled(t, s, func(p Pending) bool { return p.Attempts == i+1 })
		if !s.retrying() {
			t.Fatal("should stay up for the retry")
		}
		c.add(wait - time.Second)
		s.sweep()
		expect(t, got, "") // not yet
		c.add(time.Second)
		s.sweep()
		expect(t, got, "@ai_paul:hs !r:hs retry")
	}
	select {
	case <-alerts:
	case <-time.After(time.Second):
		t.Fatal("no desktop alert after the last retry")
	}
	settled(t, s, func(p Pending) bool { return p.NextAt == 0 && p.Attempts == 0 })
	if s.retrying() {
		t.Fatal("given up: nothing to retry")
	}
}

func TestInFlightIsRetriedAfterACrash(t *testing.T) {
	fail := false
	s, got, c, _ := sweepServer(t, true, time.Now(), &fail)
	s.save("@ai_paul:hs", Pending{Room: "!r:hs", NextAt: c.now().Add(-time.Second).UnixMilli()})
	s.sweep()
	expect(t, got, "@ai_paul:hs !r:hs retry")
	settled(t, s, func(p Pending) bool { return p.DeliveredAt > 0 }) // the worker is done writing
}

func TestReadElsewhereIsDropped(t *testing.T) {
	fail := false
	s, got, c, _ := sweepServer(t, false, time.Now(), &fail)
	s.save("@ai_paul:hs", Pending{Room: "!r:hs", Attempts: 1, NextAt: c.now().Add(-time.Second).UnixMilli()})
	s.sweep()
	expect(t, got, "")
	if p := listPending(s.state); len(p) != 0 {
		t.Fatalf("pending %v", p)
	}
}

func TestParseVoice(t *testing.T) {
	if v, _ := parseVoice("em_alex"); v != "em_alex" {
		t.Fatalf("%v", v)
	}
	v, err := parseVoice("em_alex=0.7,em_santa=0.3")
	if m, ok := v.(map[string]float64); err != nil || !ok || m["em_alex"] != 0.7 || m["em_santa"] != 0.3 {
		t.Fatalf("%v %v", v, err)
	}
	if _, err := parseVoice("em_alex=x"); err == nil {
		t.Fatal("bad weight accepted")
	}
}

func TestMutedKeepsThenCatchesUpOnUnmute(t *testing.T) {
	fail := false
	s, got, c, _ := sweepServer(t, true, time.Now(), &fail)
	if err := saveMutes(s.state, Mutes{allMuted: 0}); err != nil {
		t.Fatal(err)
	}
	s.sweep()
	expect(t, got, "")
	body := notice(c.now(), "m1")
	post(s, body, sign("s-paul", body))
	expect(t, got, "")
	if p := s.roomState("@ai_paul:hs", "!r:hs"); p.Unread != 1 || p.NextAt != 0 {
		t.Fatalf("muted notice not kept as pending: %+v", p)
	}
	if s.retrying() {
		t.Fatal("an indefinite mute should not keep the client up")
	}
	saveMutes(s.state, Mutes{})
	s.sweep()
	expect(t, got, "@ai_paul:hs !r:hs catch-up")
	settled(t, s, func(p Pending) bool { return p.DeliveredAt > 0 })
	s.sweep()
	expect(t, got, "")
}

func TestTimedMuteEndsByItself(t *testing.T) {
	fail := false
	s, got, c, _ := sweepServer(t, true, time.Now(), &fail)
	saveMutes(s.state, Mutes{"@ai_paul:hs": c.now().Add(10 * time.Minute).UnixMilli()})
	s.sweep()
	expect(t, got, "")
	if !s.retrying() {
		t.Fatal("a timed mute should keep the client up")
	}
	c.add(11 * time.Minute)
	s.sweep()
	expect(t, got, "@ai_paul:hs !r:hs catch-up")
}

func TestIdentitySetUpWithoutWebhook(t *testing.T) {
	path := t.TempDir() + "/config.json"
	if err := addEntity(path, "@ai_x:hs", "x.env"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := loadConfig(path)
	cfg.Entities["@ai_x:hs"].Secret, cfg.Entities["@ai_x:hs"].OnNotice = "s", []string{"/bin/true"}
	saveConfig(path, cfg)
	addEntity(path, "@ai_x:hs", "y.env")
	cfg, _ = loadConfig(path)
	if e := cfg.Entities["@ai_x:hs"]; !e.hasWebhook() || !strings.HasSuffix(e.TokenFile, "y.env") {
		t.Fatalf("overwritten: %+v", e)
	}
}

func TestNewWebhookCatchesUp(t *testing.T) {
	fail := false
	s, got, _, _ := sweepServer(t, true, time.Now(), &fail)
	config, registered := s.config, false
	s.config = func() (*Config, error) {
		cfg, err := config()
		if err != nil || registered {
			return cfg, err
		}
		paul := *cfg.Entities["@ai_paul:hs"]
		paul.Secret = ""
		return &Config{Entities: map[string]*Entity{"@ai_paul:hs": &paul}}, nil
	}
	s.sweep()
	expect(t, got, "")
	registered = true
	s.sweep()
	expect(t, got, "@ai_paul:hs !r:hs catch-up")
}
