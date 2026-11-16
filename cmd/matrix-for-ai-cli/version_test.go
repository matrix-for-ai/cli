package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckUpdate(t *testing.T) {
	latest := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/matrix-for-ai/cli/releases/tag/v"+latest, http.StatusFound)
	}))
	defer srv.Close()
	defer func(u string) { releasesURL = u }(releasesURL)
	releasesURL = srv.URL
	for v, want := range map[string]string{version: "up to date\n", "99.0.0": "99.0.0 is out: " + srv.URL + "/tag/v99.0.0\n"} {
		latest = v
		var out strings.Builder
		if err := checkUpdate(&out); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), want) {
			t.Errorf("latest %s: %q", v, out.String())
		}
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1.1.0", "1.0.0", true}, {"1.0.0", "1.0.0", false}, {"1.10.0", "1.9.0", true}, {"1.0.9", "1.1.0", false}} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
