package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func testServer(t *testing.T, now time.Time) (*server, chan string) {
	cfg := &Config{Entities: map[string]*Entity{
		"@ai_ringo:hs": {Secret: "s-ringo", OnNotice: []string{"/bin/true"}},
		"@ai_paul:hs":  {Secret: "s-paul", OnNotice: []string{"/bin/true"}},
	}}
	got := make(chan string, 8)
	s := newServer(func() (*Config, error) { return cfg, nil }, t.TempDir())
	t.Cleanup(func() { // before TempDir's: a worker may still be writing there
		for i := 0; i < 1000; i++ {
			s.mu.Lock()
			busy := s.pending > 0
			s.mu.Unlock()
			if !busy {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	s.fetch = func(Entity) (map[string]RoomUnread, error) { return nil, nil }
	s.now = func() time.Time { return now }
	s.run = func(name string, _ Entity, n Notice) error { got <- name + " " + n.Room; return nil }
	s.alert = func(string, string) {}
	return s, got
}

func post(s *server, body []byte, sig string) int {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	if sig != "" {
		req.Header.Set("X-Signature-256", sig)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w.Code
}

func notice(now time.Time, id string) []byte {
	b, _ := json.Marshal(Notice{Identity: "@ai_paul:hs", Room: "!r:hs", Sender: "@alice:hs",
		Unread: 1, TS: now.UnixMilli(), DeliveryID: id})
	return b
}

func TestDeliversToEntityWhoseSecretSigns(t *testing.T) {
	now := time.Now()
	s, got := testServer(t, now)
	body := notice(now, "d1")
	if c := post(s, body, sign("s-paul", body)); c != http.StatusNoContent {
		t.Fatalf("status %d", c)
	}
	if g := <-got; g != "@ai_paul:hs !r:hs" {
		t.Fatalf("delivered to %q", g)
	}
}

func TestRejects(t *testing.T) {
	now := time.Now()
	s, got := testServer(t, now)
	body := notice(now, "d1")
	old := notice(now.Add(-6*time.Minute), "d2")
	cases := map[string]struct {
		body []byte
		sig  string
		want int
	}{
		"no signature":      {body, "", http.StatusUnauthorized},
		"wrong secret":      {body, sign("otro", body), http.StatusUnauthorized},
		"tampered body":     {append([]byte(" "), body...), sign("s-paul", body), http.StatusUnauthorized},
		"identity mismatch": {body, sign("s-ringo", body), http.StatusUnauthorized},
		"stale ts":          {old, sign("s-paul", old), http.StatusBadRequest},
	}
	for name, c := range cases {
		if code := post(s, c.body, c.sig); code != c.want {
			t.Errorf("%s: status %d, want %d", name, code, c.want)
		}
	}
	select {
	case g := <-got:
		t.Fatalf("delivered %q, should not have", g)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDuplicateDeliveryIDIsDeliveredOnce(t *testing.T) {
	now := time.Now()
	s, got := testServer(t, now)
	body := notice(now, "d1")
	post(s, body, sign("s-paul", body))
	if c := post(s, body, sign("s-paul", body)); c != http.StatusNoContent {
		t.Fatalf("duplicate should give 204, got %d", c)
	}
	<-got
	select {
	case g := <-got:
		t.Fatalf("delivered twice: %q", g)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestReadEnvFile(t *testing.T) {
	p := t.TempDir() + "/m.env"
	writeFile(t, p, "# c\nexport MATRIX_AI_BASE=\"https://x/ai/\"\nMATRIX_AI_TOKEN=abc\nMATRIX_AI_MXID=@a:hs\n")
	base, token, mxid, err := appserviceCredentials(&Entity{TokenFile: p})
	if err != nil || base != "https://x/ai" || token != "abc" || mxid != "@a:hs" {
		t.Fatalf("%q %q %v", base, token, err)
	}
}

func writeFile(t *testing.T, p, s string) {
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A handler that leaves a child in the background, output and all, has delivered when it exits 0.
func TestRunOnNoticeBackgroundChild(t *testing.T) {
	start := time.Now()
	err := runOnNotice("x", Entity{OnNotice: []string{"sh", "-c", "sleep 5 & echo hi; exit 0"}}, Notice{})
	if err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

func TestDuplicateDeliveryIDAfterRestart(t *testing.T) {
	now := time.Now()
	s, got := testServer(t, now)
	body := notice(now, "d1")
	post(s, body, sign("s-paul", body))
	<-got
	restarted := newServer(s.config, s.state)
	restarted.now, restarted.run, restarted.fetch, restarted.alert = s.now, s.run, s.fetch, s.alert
	post(restarted, body, sign("s-paul", body))
	select {
	case g := <-got:
		t.Fatalf("a replay after a restart was delivered: %q", g)
	case <-time.After(50 * time.Millisecond):
	}
	later := now.Add(2*maxSkew + time.Second)
	restarted.now = func() time.Time { return later }
	if restarted.firstTime("d2"); len(loadSeen(s.state)) != 1 {
		t.Fatalf("stale ids are not pruned: %v", loadSeen(s.state))
	}
}
