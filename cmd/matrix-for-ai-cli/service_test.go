package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdUnits(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "100% config.json")
	if err := saveConfig(cfg, &Config{URL: "http://192.0.2.1:8794/"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin")
	socket, svc, err := systemdService{"/home/me/bin/matrix-for-ai-cli", cfg, ""}.units()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(socket, "ListenStream=192.0.2.1:8794\n") {
		t.Errorf("socket:\n%s", socket)
	}
	want := `ExecStart="/home/me/bin/matrix-for-ai-cli" --config "` + strings.ReplaceAll(cfg, "%", "%%") + `" serve --idle 15m`
	if !strings.Contains(svc, want+"\n") || !strings.Contains(svc, `Environment="PATH=/home/me/bin:/usr/bin"`) {
		t.Errorf("service:\n%s\nwant %s", svc, want)
	}
}

func TestSystemdUnitsWantedBy(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := saveConfig(cfg, &Config{URL: "http://192.0.2.1:8794/"}); err != nil {
		t.Fatal(err)
	}
	socket, svc, err := systemdService{"/bin/c", cfg, "graphical-session.target"}.units()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{socket, svc} {
		for _, want := range []string{"PartOf=graphical-session.target\n", "After=graphical-session.target\n", "WantedBy=graphical-session.target\n"} {
			if !strings.Contains(u, want) {
				t.Errorf("missing %q in:\n%s", want, u)
			}
		}
	}
}
