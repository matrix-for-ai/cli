package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// identity token: with its own token it asks for "me"; with the admin's, for the MXID; either way the
// new token replaces the old one in the identity's file.
func TestIdentityToken(t *testing.T) {
	const mxid = "@ai_nova_ab12cd:example.org"
	var asked []string
	srv := contractAppservice(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Header.Get("Authorization")+" "+r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"mxid":"` + mxid + `","api_token":"new"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	tf := filepath.Join(dir, "nova.env")
	os.WriteFile(tf, []byte("MATRIX_AI_BASE="+srv.URL+"\nMATRIX_AI_TOKEN=old\nROOM=!r:example.org\nMATRIX_AI_MXID="+mxid+"\n"), 0o600)
	if err := addEntity(config, mxid, tf); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MATRIX_AI_ADMIN_TOKEN", "")
	if err := cmdIdentityToken(config, []string{mxid}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(tf); !strings.Contains(string(b), "MATRIX_AI_TOKEN=new\nROOM=!r:example.org\n") {
		t.Errorf("token file: %s", b)
	}
	t.Setenv("MATRIX_AI_ADMIN_TOKEN", "adm")
	if err := cmdIdentityToken(config, []string{mxid}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Bearer old /identities/me/token", "Bearer adm /identities/%40ai_nova_ab12cd%3Aexample.org/token"}
	if strings.Join(asked, "|") != strings.Join(want, "|") {
		t.Errorf("asked %q, want %q", asked, want)
	}
	if err := cmdIdentityToken(config, []string{"@ai_other_x:example.org"}); err == nil {
		t.Error("one not set up here, without --base: no error")
	}
}

func TestCheckAppserviceBaseInsecure(t *testing.T) {
	if err := checkAppserviceBase("x", "http://appservice.invalid", false); !errors.Is(err, errUsage) {
		t.Fatalf("plain http without --insecure: %v", err)
	}
	if err := checkAppserviceBase("x", "http://appservice.invalid", true); !errors.Is(err, errAppserviceUnreachable) {
		t.Fatalf("--insecure should get past the http check to the appservice: %v", err)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "127.1.2.3", "::1"} {
		if !isLoopback(host) {
			t.Errorf("%s is this machine", host)
		}
	}
	for _, host := range []string{"192.0.2.10", "localhost.example.org", "appservice.invalid"} {
		if isLoopback(host) {
			t.Errorf("%s is not this machine", host)
		}
	}
}
