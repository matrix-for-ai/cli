package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestDeliverToListeningSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	if err := send(path, Delivery{Room: "!r:hs"}); !errors.Is(err, errNoListener) {
		t.Fatalf("no listener: %v", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var out syncBuf
	quit := make(chan bool)
	go func() { quit <- serveSession(ln, &out) }()

	want := Delivery{Identity: "@ai_ringo:hs", Room: "!r:hs", Sender: "@alice:hs", Unread: 2, Reason: "notice", ReplyIfStopped: true, Command: "compact"}
	if err := send(path, want); err != nil {
		t.Fatal(err)
	}
	var got Delivery
	if err := json.Unmarshal(out.b.Bytes(), &got); err != nil || got != want {
		t.Fatalf("got %+v (%v)", got, err)
	}
	if err := send(path, Delivery{Quit: true}); err == nil {
		t.Fatal("quit should not be acknowledged as a delivery")
	}
	select {
	case q := <-quit:
		if !q {
			t.Fatal("should report the takeover")
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not quit")
	}
}

func TestSocketPathRejectsBadIDs(t *testing.T) {
	for _, id := range []string{"", "../x", "a/b", "a b"} {
		if _, err := socketPath("deliver", id); err == nil {
			t.Errorf("%q accepted", id)
		}
	}
}

func TestRuntimeDirInSharedTempMustBePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the temp dir is the user's own")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", tmp)
	dir := filepath.Join(tmp, "matrix-for-ai-cli-"+strconv.Itoa(os.Getuid()))

	if got, err := runtimeDir(); err != nil || got != dir {
		t.Fatalf("new: %q %v", got, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDir(); !errors.Is(err, errSystem) {
		t.Fatalf("mode 755: %v", err)
	}
	os.Remove(dir)
	real := filepath.Join(tmp, "elsewhere")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDir(); !errors.Is(err, errSystem) {
		t.Fatalf("symlink: %v", err)
	}
}
