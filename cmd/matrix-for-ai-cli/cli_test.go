package main

// The CLI as its user sees it: each file in testdata/cli runs one command line against a fake appservice and
// holds what it must print and exit with. go test -run TestCLI -update rewrites them, so a changed
// message shows up as a diff. A case's "error KIND" also runs it in-process and checks errors.Is.
//
// A case file:
//
//	# what it shows
//	run ARGS...        words, with '…' or "…" quoting; $WORK and $BOT are replaced
//	env K=V            (repeatable) MATRIX_AI_MXID is nova unless set here
//	stdin TEXT         \n for newlines
//	file PATH TEXT     write $WORK/PATH first (\n for newlines)
//	error KIND         a key of errorKinds
//	refuse ID          a session listens as ID but does not take what it is handed
//	== exit
//	== stdout
//	== stderr

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/cli with what the commands print now")

func TestMain(m *testing.M) {
	if os.Getenv("MFAC_MAIN") == "1" { // a case running the client
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const nova = "@ai_nova_ab12cd:example.org"

// fakeAppservice answers like the appservice, for the cases: one token per behaviour, rooms named for theirs.
// What it answers is held to the appservice's contract (contract_test.go), except where a case needs an appservice
// or a proxy answering off it.
func fakeAppservice(t testing.TB, report func(string)) *httptest.Server {
	answer := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
	problem := func(w http.ResponseWriter, status int, code, detail string) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"type":"about:blank","title":%q,"status":%d,"code":%q,"detail":%q}`,
			http.StatusText(status), status, code, detail)
	}
	const message = `{"event_id":%q,"room":"!r:example.org","sender":"@alice:example.org","ts":1,"type":"text","content":{"text":%q}}`
	return httptest.NewServer(appserviceContract(t).checked(likeTheAppservice(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		path := r.URL.EscapedPath()
		if token == "dead" {
			problem(w, 401, "unauthorized", "invalid or revoked token")
			return
		}
		room := ""
		if rest, ok := strings.CutPrefix(path, "/channels/"); ok {
			room, _ = url.QueryUnescape(strings.SplitN(rest, "/", 2)[0])
			if !strings.HasPrefix(room, "!") {
				w.Header().Set(offContract, "the client hands the appservice what it was given, and the appservice refuses it")
				problem(w, 400, "invalid_room", "not a room id: "+room)
				return
			}
		}
		switch {
		case path == "/capabilities":
			answer(w, 200, `{"api_version":2,"open_signup":true,"webhooks":true,"e2ee":true,`+
				`"tts":{"available":true,"info":{"voices":["em_alex","em_santa"]}},"stt":{"available":true,"info":{}}}`)
		case path == "/channels" && r.Method == http.MethodGet:
			answer(w, 200, `{"channels":[{"room":"!r:example.org","encrypted":true,"unread":2,"last_unread":{"event_id":"$e","sender":"@alice:example.org","ts":1},"last_ts":1}],"next_cursor":null}`)
		case path == "/channels":
			answer(w, 201, `{"room":"!new:example.org","name":null,"topic":null,"direct":false,"public":false,"encrypted":true,"invited":[]}`)
		case room == "!notmine:example.org":
			problem(w, 404, "not_member", "not a member of "+room)
		case room == "!future:example.org":
			w.Header().Set(offContract, "an appservice of a newer major")
			w.Header().Set("X-API-Version", "4")
			answer(w, 200, `{"events":[],"has_more":false}`)
		case room == "!busy:example.org":
			w.Header().Set("Retry-After", "30")
			problem(w, 429, "rate_limited", "at most 300 calls per minute; retry in 30s")
		case room == "!down:example.org":
			w.Header().Set(offContract, "a proxy in front of the appservice")
			w.WriteHeader(502)
			w.Write([]byte("Bad Gateway"))
		case room == "!odd:example.org":
			w.Header().Set(offContract, "an appservice answering what this client does not expect")
			answer(w, 200, `{}`)
		case strings.HasSuffix(path, "/receipt") && room == "!noreceipt:example.org":
			problem(w, 502, "homeserver_error", "the homeserver failed")
		case strings.HasSuffix(path, "/receipt"):
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/events") && r.Method == http.MethodGet:
			if r.URL.Query().Get("unread") == "true" {
				answer(w, 200, `{"events":[`+fmt.Sprintf(message, "$e", "hi")+`],"has_more":false}`)
				return
			}
			answer(w, 200, `{"events":[`+fmt.Sprintf(message, "$h", r.URL.RawQuery)+`],"has_more":true}`)
		case strings.HasSuffix(path, "/members") && r.Method == http.MethodGet:
			answer(w, 200, `{"members":[{"user":"@alice:example.org","display_name":"Alice","avatar_url":null}],"next_cursor":null}`)
		case strings.HasSuffix(path, "/members"):
			var body struct{ User string }
			jsonBody(r, &body)
			answer(w, 201, fmt.Sprintf(`{"user":%q,"membership":"invite"}`, body.User))
		case room != "" && r.Method == http.MethodGet && strings.Contains(path, "/events/"):
			answer(w, 200, fmt.Sprintf(message, "$e", "hi"))
		case room != "" && strings.HasSuffix(path, "/events"):
			answer(w, 201, `{"event_id":"$new","type":"text","encrypted":true}`)
		case room != "" && r.Method == http.MethodDelete:
			w.WriteHeader(204)
		case room != "" && r.Method == http.MethodPut:
			answer(w, 201, fmt.Sprintf(`{"room":%q,"encrypted":true,"unread":0,"last_unread":null,"last_ts":null}`, room))
		case path == "/identities" && r.Method == http.MethodPost:
			var body struct{ Name string }
			jsonBody(r, &body)
			switch body.Name {
			case "closed":
				problem(w, 403, "signup_closed", "signup is closed")
			case "mute":
				w.Header().Set(offContract, "an appservice answering what this client does not expect")
				answer(w, 201, `{}`)
			default:
				answer(w, 201, fmt.Sprintf(`{"mxid":"@ai_%s_xyz123:example.org","localpart":"ai_%[1]s_xyz123","display_name":%[1]q,"api_token":"t-%[1]s"}`, body.Name))
			}
		case strings.HasSuffix(path, "/token") && r.Method == http.MethodDelete:
			if strings.Contains(path, "revoked") {
				problem(w, 404, "token_not_found", "the identity has no token")
				return
			}
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/token") && r.Method == http.MethodPost:
			id, _ := url.QueryUnescape(strings.TrimSuffix(strings.TrimPrefix(path, "/identities/"), "/token"))
			if id == "me" {
				id = nova
			}
			answer(w, 200, fmt.Sprintf(`{"mxid":%q,"api_token":"rotated"}`, id))
		case path == "/identities/me/webhook" && r.Method == http.MethodPut:
			var body struct{ URL string }
			jsonBody(r, &body)
			answer(w, 200, fmt.Sprintf(`{"url":%q,"secret":"s3cret"}`, body.URL))
		case path == "/identities/me/webhook":
			w.WriteHeader(204)
		case path == "/identities/me/avatar" && r.Method == http.MethodPut:
			answer(w, 201, `{"avatar_url":"mxc://example.org/a"}`)
		case path == "/identities/me/avatar":
			w.WriteHeader(204)
		case path == "/identities/me":
			answer(w, 200, `{"mxid":"`+nova+`","localpart":"ai_nova_ab12cd","matrix":{"display_name":null,"avatar_url":null},"tts":{},"stt":{},"transcribe_audio":true}`)
		case strings.HasPrefix(path, "/elsewhere/"):
			w.Header().Set(offContract, "a server that is not the appservice")
			problem(w, 404, "not_found", "no such route: "+path)
		default:
			problem(w, 404, "not_found", "no such route: "+path)
		}
	})), report))
}

func jsonBody(r *http.Request, v any) {
	data, _ := io.ReadAll(r.Body)
	json.Unmarshal(data, v)
}

// cliWorld writes the config and token files every case starts from, in a fresh folder.
func cliWorld(t *testing.T, appservice string) string {
	work, err := os.MkdirTemp("", "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	files := map[string]string{
		"nova.env": "MATRIX_AI_BASE=" + appservice + "\nMATRIX_AI_TOKEN=good\nMATRIX_AI_MXID=" + nova + "\n",
		"dead.env": "MATRIX_AI_BASE=" + appservice + "\nMATRIX_AI_TOKEN=dead\nMATRIX_AI_MXID=@ai_dead_ab12cd:example.org\n",
		"half.env": "MATRIX_AI_BASE=" + appservice + "\nMATRIX_AI_MXID=@ai_half_ab12cd:example.org\n",
		"config.json": `{"url": "http://127.0.0.1:1/", "entities": {
  "` + nova + `": {"token_file": "` + work + `/nova.env"},
  "@ai_dead_ab12cd:example.org": {"token_file": "` + work + `/dead.env"},
  "@ai_half_ab12cd:example.org": {"token_file": "` + work + `/half.env"},
  "@ai_twin_ab12cd:example.org": {"token_file": "` + work + `/nova.env"},
  "@ai_gone_ab12cd:example.org": {"token_file": "` + work + `/gone.env"}}}
`,
	}
	for name, s := range files {
		if err := os.WriteFile(filepath.Join(work, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return work
}

type cliCase struct {
	args     []string
	env      []string
	stdin    string
	files    [][2]string
	kind     string
	refuse   string // a session listening as this ID that does not take deliveries
	sections map[string]string
	header   string // the lines before the sections, kept as written
}

func readCase(t *testing.T, path string) cliCase {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c := cliCase{sections: map[string]string{}}
	head, body, _ := strings.Cut(string(data), "\n== ")
	c.header = head + "\n"
	for _, line := range strings.Split(head, "\n") {
		word, rest, _ := strings.Cut(line, " ")
		switch word {
		case "run":
			c.args = words(t, rest)
		case "env":
			c.env = append(c.env, rest)
		case "stdin":
			c.stdin = strings.ReplaceAll(rest, `\n`, "\n")
		case "file":
			name, text, _ := strings.Cut(rest, " ")
			c.files = append(c.files, [2]string{name, strings.ReplaceAll(text, `\n`, "\n")})
		case "error":
			c.kind = rest
		case "refuse":
			c.refuse = rest
		case "", "#":
		default:
			if !strings.HasPrefix(line, "#") {
				t.Fatalf("%s: unknown line %q", path, line)
			}
		}
	}
	if body != "" {
		name := ""
		for _, line := range strings.SplitAfter("== "+body, "\n") {
			if s, ok := strings.CutPrefix(line, "== "); ok {
				name = strings.TrimSuffix(s, "\n")
				c.sections[name] = ""
				continue
			}
			c.sections[name] += line
		}
	}
	return c
}

// words splits a run line like a shell would, for '…' and "…" only.
func words(t *testing.T, s string) []string {
	var out []string
	var cur strings.Builder
	in, quote := false, rune(0)
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ':
			if in {
				out, in = append(out, cur.String()), false
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		t.Fatalf("unclosed quote in %q", s)
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

func (c cliCase) environ(work, appservice string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + work, "XDG_CONFIG_HOME=" + work + "/xdg",
		"XDG_STATE_HOME=" + work + "/state", "XDG_RUNTIME_DIR=" + work + "/run",
		"MATRIX_FOR_AI_CLI_CONFIG=" + work + "/config.json", "MATRIX_AI_MXID=" + nova}
	for _, e := range c.env {
		env = append(env, expand(e, work, appservice))
	}
	return env
}

func expand(s, work, appservice string) string {
	return strings.NewReplacer("$WORK", work, "$BOT", appservice).Replace(s)
}

func (c cliCase) prepare(t *testing.T, work, appservice string) []string {
	for _, f := range c.files {
		p := filepath.Join(work, f[0])
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(expand(f[1], work, appservice)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if c.refuse != "" {
		refuser(t, filepath.Join(work, "run", "matrix-for-ai-cli", c.refuse+".sock"))
	}
	args := make([]string, len(c.args))
	for i, a := range c.args {
		args[i] = expand(a, work, appservice)
	}
	return args
}

func TestCLI(t *testing.T) {
	var mu sync.Mutex
	var violations []string
	appservice := fakeAppservice(t, func(v string) { mu.Lock(); violations = append(violations, v); mu.Unlock() })
	drain := func(t *testing.T) {
		mu.Lock()
		defer mu.Unlock()
		for _, v := range violations {
			t.Errorf("off the appservice's contract: %s", v)
		}
		violations = nil
	}
	defer appservice.Close()
	paths, _ := filepath.Glob("testdata/cli/*.txt")
	if len(paths) == 0 {
		t.Fatal("no cases in testdata/cli")
	}
	covered := map[string]bool{}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".txt")
		t.Run(name, func(t *testing.T) {
			c := readCase(t, path)
			work := cliWorld(t, appservice.URL)
			args := c.prepare(t, work, appservice.URL)
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(c.environ(work, appservice.URL), "MFAC_MAIN=1")
			cmd.Stdin = strings.NewReader(c.stdin)
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
			clean := strings.NewReplacer(work, "$WORK", appservice.URL, "$BOT").Replace
			got := map[string]string{"exit": strconv.Itoa(code) + "\n", "stdout": clean(stdout.String()),
				"stderr": clean(stderr.String())}
			if *update {
				var b strings.Builder
				b.WriteString(c.header)
				for _, s := range []string{"exit", "stdout", "stderr"} {
					b.WriteString("== " + s + "\n" + got[s])
				}
				if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, s := range []string{"exit", "stdout", "stderr"} {
					if got[s] != c.sections[s] {
						t.Errorf("%s:\n--- got\n%s--- want\n%s", s, got[s], c.sections[s])
					}
				}
			}
			if c.kind != "" {
				covered[c.kind] = true
				checkKind(t, c, appservice.URL, code)
			}
			drain(t)
		})
	}
	if flag.Lookup("test.run").Value.String() != "" {
		return // only some cases ran
	}
	// What no case can provoke here, and why.
	unprovokable := map[string]string{"unsupported": "service, on an OS other than Linux, macOS or Windows"}
	var missing []string
	for k := range errorKinds {
		if !covered[k] && unprovokable[k] == "" {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("error kinds no case provokes: %v", missing)
	}
}

// checkKind runs the case again in-process, in a fresh world, for errors.Is and the exit code.
func checkKind(t *testing.T, c cliCase, appservice string, code int) {
	kind, ok := errorKinds[c.kind]
	if !ok {
		t.Fatalf("unknown error kind %q", c.kind)
	}
	if code == 0 {
		t.Fatalf("error %s: exited 0", c.kind)
	}
	work := cliWorld(t, appservice)
	args := c.prepare(t, work, appservice)
	for _, kv := range c.environ(work, appservice) {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	null, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer null.Close()
	r, w, _ := os.Pipe()
	w.WriteString(c.stdin)
	w.Close()
	stdin, stdout, stderr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = r, null, null
	log.SetOutput(io.Discard)
	err := cli(args)
	os.Stdin, os.Stdout, os.Stderr = stdin, stdout, stderr
	log.SetOutput(os.Stderr)
	if !errors.Is(err, kind) {
		t.Errorf("error %q is not %s", err, c.kind)
	}
	for k, other := range errorKinds {
		if k != c.kind && errors.Is(err, other) && !(c.kind == "partial" && k == "appservice") {
			t.Errorf("error %q is %s too", err, k)
		}
	}
	if exitCode(err) != code {
		t.Errorf("exitCode %d, but it exited %d", exitCode(err), code)
	}
}

// refuser listens at path and answers every delivery with something other than ok.
func refuser(t *testing.T, path string) {
	os.MkdirAll(filepath.Dir(path), 0o700)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			bufio.NewReader(c).ReadString('\n')
			c.Write([]byte("stopped\n"))
			c.Close()
		}
	}()
}
