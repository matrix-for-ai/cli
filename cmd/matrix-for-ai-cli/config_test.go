package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// What another command saves while webhook create waits on the appservice stays.
func TestWebhookCreateKeepsConcurrentChanges(t *testing.T) {
	const mxid = "@ai_nova_ab12cd:example.org"
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	tf := filepath.Join(dir, "nova.env")
	srv := contractAppservice(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := addEntity(config, "@ai_other_ab12cd:example.org", tf); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"url":"http://192.0.2.10:8794/","secret":"s"}`))
	}))
	defer srv.Close()
	os.WriteFile(tf, []byte("MATRIX_AI_BASE="+srv.URL+"\nMATRIX_AI_TOKEN=t\nMATRIX_AI_MXID="+mxid+"\n"), 0o600)
	if err := addEntity(config, mxid, tf); err != nil {
		t.Fatal(err)
	}
	if err := cmdWebhookCreate(config, []string{"--as", mxid, "--url", "http://192.0.2.10:8794/", "/bin/true"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Entities["@ai_other_ab12cd:example.org"]; !ok {
		t.Error("the identity set up meanwhile is gone")
	}
	if !cfg.Entities[mxid].hasWebhook() {
		t.Error("no webhook saved")
	}
}

// A config from before 1.1.0: its url's port is where the receiver listens, and the url goes at the next save.
func TestOldConfigURL(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"url": "http://192.0.2.10:9000/", "entities": {}}`), 0o600)
	if err := updateConfig(config, func(*Config) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(config)
	if got, want := string(b), "{\n  \"listen\": \"0.0.0.0:9000\",\n  \"entities\": {}\n}\n"; got != want {
		t.Errorf("saved %s, want %s", got, want)
	}
}
