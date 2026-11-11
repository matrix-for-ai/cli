package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

type Delivery struct {
	Identity       string `json:"identity"`
	Room           string `json:"room"`
	Sender         string `json:"sender"`
	Unread         int    `json:"unread"`
	Reason         string `json:"reason"`
	ReplyIfStopped bool   `json:"reply_if_stopped,omitempty"`
	Command        string `json:"command,omitempty"` // for the harness to run instead of a prompt; the handler vouches for it
	Quit           bool   `json:"quit,omitempty"`    // a newer listener for the same session takes over
}

var deliveryID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var commandName = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

func socketPath(command, id string) (string, error) {
	if id == "" {
		return "", usageError(command, "missing --id")
	}
	if !deliveryID.MatchString(id) {
		return "", usageError(command, "--id: %q is not an ID (A-Z, a-z, 0-9, _ and -, up to 128)", id)
	}
	dir, err := runtimeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+".sock")
	if len(path) > 100 { // sun_path is 104-108 bytes depending on the OS
		return "", fail(errSystem, "socket path longer than 100 bytes: %s", path)
	}
	return path, nil
}

func runtimeDir() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	switch {
	case dir != "":
		dir = filepath.Join(dir, "matrix-for-ai-cli")
	case runtime.GOOS == "windows": // the temp dir is already the user's own
		dir = filepath.Join(os.TempDir(), "matrix-for-ai-cli")
	default: // a shared temp dir: anyone could have made it first
		dir = filepath.Join(os.TempDir(), "matrix-for-ai-cli-"+strconv.Itoa(os.Getuid()))
		return dir, privateDir(dir)
	}
	return dir, os.MkdirAll(dir, 0o700)
}

func cmdListen(args []string) error {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	id := fs.String("id", "", "")
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}
	path, err := socketPath(fs.Name(), *id)
	if err != nil {
		return err
	}
	if send(path, Delivery{Quit: true}) == nil {
		time.Sleep(100 * time.Millisecond) // the old listener is going away
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false) // the path may be a newer listener's by then
	// Polled because nothing tells us the adapter (our parent) is gone; where orphans are not
	// reparented, the adapter ends us itself.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; ln.Close() }()
	parent := os.Getppid()
	go func() {
		for range time.Tick(5 * time.Second) {
			if os.Getppid() != parent {
				ln.Close()
				return
			}
		}
	}()
	if !serveSession(ln, os.Stdout) {
		os.Remove(path) // unless a newer listener took it over
	}
	return nil
}

func serveSession(ln net.Listener, w io.Writer) (takenOver bool) {
	out := json.NewEncoder(w)
	for {
		c, err := ln.Accept()
		if err != nil {
			return false
		}
		var d Delivery
		c.SetDeadline(time.Now().Add(5 * time.Second))
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err == nil && json.Unmarshal(line, &d) == nil {
			if d.Quit {
				c.Close()
				ln.Close()
				return true
			}
			out.Encode(d)
			c.Write([]byte("ok\n"))
		}
		c.Close()
	}
}

func send(path string, d Delivery) error {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return errNoListener
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	line, _ := json.Marshal(d)
	if _, err := c.Write(append(line, '\n')); err != nil {
		return err
	}
	ack, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || ack != "ok\n" {
		return fail(errNotTaken, "the session did not take it")
	}
	return nil
}

func cmdDeliver(args []string) error {
	fs := flag.NewFlagSet("deliver", flag.ExitOnError)
	id := fs.String("id", "", "")
	wait := fs.Duration("wait", 0, "")
	reply := fs.Bool("reply-if-stopped", false, "")
	command := fs.String("command", "", "")
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}
	path, err := socketPath(fs.Name(), *id)
	if err != nil {
		return err
	}
	unread, _ := strconv.Atoi(os.Getenv("NOTICE_UNREAD"))
	d := Delivery{Identity: os.Getenv("NOTICE_IDENTITY"), Room: os.Getenv("NOTICE_ROOM"),
		Sender: os.Getenv("NOTICE_SENDER"), Unread: unread, Reason: os.Getenv("NOTICE_REASON"),
		ReplyIfStopped: *reply, Command: *command}
	if *command != "" && !commandName.MatchString(*command) {
		return usageError(fs.Name(), "--command: %q is not a command name (a-z, 0-9, _ and -, up to 64)", *command)
	}
	if d.Identity == "" || d.Room == "" {
		return usageError(fs.Name(), "NOTICE_IDENTITY and NOTICE_ROOM are missing: run it from a handler")
	}
	deadline := time.Now().Add(*wait)
	for {
		err = send(path, d)
		if !errors.Is(err, errNoListener) || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	return err
}
