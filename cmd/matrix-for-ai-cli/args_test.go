package main

import (
	"flag"
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		args []string
		n    int
		as   string
		pos  []string
	}{
		{[]string{"ROOM", "--as", "X"}, 0, "X", []string{"ROOM"}},
		{[]string{"--as", "X", "ROOM"}, 0, "X", []string{"ROOM"}},
		{[]string{"ROOM", "--", "--as"}, 0, "", []string{"ROOM", "--as"}},
		{[]string{"ROOM", "-"}, 2, "", []string{"ROOM", "-"}},
		{[]string{"ROOM", "--as", "X", "hi", "-there", "--as", "Y"}, 2, "X", []string{"ROOM", "hi", "-there", "--as", "Y"}},
		{[]string{"--as", "X", "cmd.sh", "--as", "Y"}, 1, "X", []string{"cmd.sh", "--as", "Y"}},
	} {
		fs := flag.NewFlagSet("t", flag.ExitOnError)
		as := fs.String("as", "", "")
		parseUntil(fs, c.args, c.n)
		if *as != c.as || !slices.Equal(fs.Args(), c.pos) {
			t.Errorf("%q: as %q, args %q; want %q, %q", c.args, *as, fs.Args(), c.as, c.pos)
		}
	}
}

func TestNeedAndNotHere(t *testing.T) {
	fs := flag.NewFlagSet("channel invite", flag.ExitOnError)
	parse(fs, []string{"ROOM"})
	if err := need(fs, "ROOM", "USER"); err == nil || !strings.Contains(err.Error(), "missing USER") {
		t.Errorf("need: %v", err)
	}
	cfg := &Config{Entities: map[string]*Entity{"@ai_x_d5sn2q:example.org": {}}}
	if err := notHere(cfg, "@ai_x_d5sn_2q:example.org"); !strings.Contains(err.Error(), "did you mean @ai_x_d5sn2q:example.org?") {
		t.Errorf("notHere: %v", err)
	}
}
