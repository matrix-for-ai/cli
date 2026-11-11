package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The client against a real appservice and homeserver (a disposable one: it creates identities and rooms).
// ./build.sh e2e URL runs it; without MATRIX_AI_E2E_BASE it is skipped.
func TestE2E(t *testing.T) {
	base := os.Getenv("MATRIX_AI_E2E_BASE")
	if base == "" {
		t.Skip("set MATRIX_AI_E2E_BASE to run against an appservice")
	}
	work := t.TempDir()
	env := []string{"MFAC_MAIN=1", "PATH=" + os.Getenv("PATH"), "HOME=" + work,
		"XDG_STATE_HOME=" + work + "/state", "XDG_RUNTIME_DIR=" + work + "/run",
		"MATRIX_FOR_AI_CLI_CONFIG=" + work + "/config.json"}
	if admin := os.Getenv("MATRIX_AI_ADMIN_TOKEN"); admin != "" {
		env = append(env, "MATRIX_AI_ADMIN_TOKEN="+admin)
	}
	run := func(args ...string) (string, string, int) {
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		return stdout.String(), stderr.String(), code
	}
	ok := func(args ...string) string {
		t.Helper()
		out, errOut, code := run(args...)
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s", strings.Join(args, " "), code, errOut)
		}
		return out
	}
	okJSON := func(v any, args ...string) {
		t.Helper()
		if err := json.Unmarshal([]byte(ok(args...)), v); err != nil {
			t.Fatalf("%s: %v", strings.Join(args, " "), err)
		}
	}
	fails := func(code string, args ...string) {
		t.Helper()
		_, errOut, exit := run(args...)
		if exit != 1 || !strings.Contains(errOut, "("+code+")") {
			t.Errorf("%s: want exit 1 with (%s), got %d\n%s", strings.Join(args, " "), code, exit, errOut)
		}
	}
	create := func(name string) string {
		t.Helper()
		mxid, _, _ := strings.Cut(ok("identity", "create", "--base", base, name), "\t")
		return mxid
	}

	alice, bob := create("alice"), create("bob")
	var created struct {
		Room    string   `json:"room"`
		Invited []string `json:"invited"`
	}
	okJSON(&created, "channel", "create", "--as", alice, bob)
	room := created.Room
	if !strings.HasPrefix(room, "!") || len(created.Invited) != 1 || created.Invited[0] != bob {
		t.Fatalf("channel create: %+v", created)
	}

	var sent struct {
		EventID   string `json:"event_id"`
		Encrypted bool   `json:"encrypted"`
	}
	okJSON(&sent, "send", "--as", alice, room, "hello", "bob")
	if !strings.HasPrefix(sent.EventID, "$") || !sent.Encrypted {
		t.Fatalf("send: %+v", sent)
	}

	type channel struct {
		Room       string `json:"room"`
		Unread     int    `json:"unread"`
		LastUnread struct {
			EventID string `json:"event_id"`
			Sender  string `json:"sender"`
		} `json:"last_unread"`
	}
	unread := func(as string) []channel {
		var u struct {
			Channels []channel `json:"channels"`
		}
		okJSON(&u, "unread", "--as", as)
		return u.Channels
	}
	var seen []channel
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		if seen = unread(bob); len(seen) > 0 && seen[0].LastUnread.EventID == sent.EventID {
			break
		}
	}
	// 2: accepting the invitation leaves the invitation itself unread, then the message.
	if len(seen) != 1 || seen[0].Room != room || seen[0].Unread != 2 ||
		seen[0].LastUnread.EventID != sent.EventID || seen[0].LastUnread.Sender != alice {
		t.Fatalf("unread for bob: %+v", seen)
	}

	type event struct {
		EventID string `json:"event_id"`
		Sender  string `json:"sender"`
		Type    string `json:"type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	var read struct {
		HasMore bool    `json:"has_more"`
		Events  []event `json:"events"`
	}
	okJSON(&read, "read", "--peek", "--as", bob, room)
	if len(read.Events) != 2 || len(unread(bob)) != 1 {
		t.Fatalf("read --peek marks nothing: %+v", read)
	}
	okJSON(&read, "read", "--as", bob, room)
	want := event{EventID: sent.EventID, Sender: alice, Type: "text"}
	want.Content.Text = "hello bob"
	if len(read.Events) != 2 || read.Events[0].Type != "membership" || read.Events[0].Sender != alice ||
		read.Events[1] != want || read.HasMore {
		t.Fatalf("read: %+v", read)
	}
	if u := unread(bob); len(u) != 0 {
		t.Fatalf("read marks it read: %+v", u)
	}
	okJSON(&read, "read", "--latest", "--limit", "1", "--as", bob, room)
	if len(read.Events) != 1 || read.Events[0] != want {
		t.Fatalf("read --latest: %+v", read)
	}
	var one event
	okJSON(&one, "event", "--as", bob, room, sent.EventID)
	if one != want {
		t.Fatalf("event: %+v", one)
	}

	var members struct {
		Members []struct {
			User string `json:"user"`
		} `json:"members"`
	}
	okJSON(&members, "channel", "members", "--as", alice, room)
	if len(members.Members) != 2 {
		t.Fatalf("channel members: %+v", members)
	}

	fails("user_not_found", "channel", "invite", "--as", alice, room, "@nobody_e2e:"+strings.SplitN(alice, ":", 2)[1])
	fails("user_not_member", "channel", "kick", "--as", alice, room, create("carol"))
	fails("already_member", "channel", "invite", "--as", alice, room, bob)
	fails("event_not_found", "event", "--as", bob, room, "$nothere")
	var group struct {
		Room string `json:"room"`
	}
	okJSON(&group, "channel", "create", "--as", alice, "--name", "e2e", bob)
	ok("channel", "kick", "--as", alice, group.Room, bob)
	var public struct {
		Room string `json:"room"`
	}
	okJSON(&public, "channel", "create", "--as", alice, "--public", "--name", "lobby")
	var joined struct {
		Room string `json:"room"`
	}
	okJSON(&joined, "channel", "join", "--as", bob, public.Room)
	if joined.Room != public.Room {
		t.Fatalf("channel join: %+v", joined)
	}
	fails("already_member", "channel", "join", "--as", bob, public.Room)
	pic := work + "/pic.png"
	var img bytes.Buffer
	png.Encode(&img, image.NewGray(image.Rect(0, 0, 8, 8)))
	if err := os.WriteFile(pic, img.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	type profile struct {
		Matrix struct {
			DisplayName *string `json:"display_name"`
			AvatarURL   *string `json:"avatar_url"`
		} `json:"matrix"`
	}
	var p profile
	okJSON(&sent, "send", "--as", alice, "--image", pic, room)
	okJSON(&one, "event", "--as", bob, room, sent.EventID)
	if one.Type != "image" {
		t.Fatalf("send --image: %+v", one)
	}
	okJSON(&p, "profile", "set", "--as", alice, "--name", "Alice", "--avatar", pic)
	if p.Matrix.DisplayName == nil || *p.Matrix.DisplayName != "Alice" || p.Matrix.AvatarURL == nil {
		t.Fatalf("profile set: %+v", p)
	}
	okJSON(&p, "profile", "set", "--as", alice, "--name", "", "--avatar", "")
	if p.Matrix.DisplayName != nil || p.Matrix.AvatarURL != nil {
		t.Fatalf("profile set, given empty: %+v", p)
	}
	ok("channel", "leave", "--as", bob, room)
	fails("not_member", "read", "--as", bob, room)
}
