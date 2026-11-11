package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Mutes: by Matrix ID, or allMuted; when it ends, in unix ms (0: never).
type Mutes map[string]int64

const allMuted = "*"

func mutesPath(state string) string { return filepath.Join(state, "mutes.json") }

func loadMutes(state string) Mutes {
	m := Mutes{}
	if data, err := os.ReadFile(mutesPath(state)); err == nil {
		json.Unmarshal(data, &m)
	}
	return m
}

func saveMutes(state string, m Mutes) error {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := mutesPath(state) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, mutesPath(state))
}

func (m Mutes) active(key string, now time.Time) bool {
	until, ok := m[key]
	return ok && (until == 0 || now.UnixMilli() < until)
}

func (m Mutes) muted(mxid string, now time.Time) bool {
	return m.active(allMuted, now) || m.active(mxid, now)
}

func (m Mutes) anyEndsByItself(now time.Time) bool {
	for k, until := range m {
		if until > 0 && m.active(k, now) {
			return true
		}
	}
	return false
}

func cmdMute(configPath, state string, args []string) error {
	fs := flag.NewFlagSet("mute", flag.ExitOnError)
	duration := fs.Duration("for", 0, "")
	parse(fs, args)
	if fs.NArg() > 1 {
		return usageError(fs.Name(), "unexpected %q", fs.Arg(1))
	}
	if *duration < 0 {
		return usageError(fs.Name(), "--for must be positive")
	}
	key, err := muteKey(configPath, fs.Arg(0))
	if err != nil {
		return err
	}
	m := loadMutes(state)
	m[key] = 0
	if *duration > 0 {
		m[key] = time.Now().Add(*duration).UnixMilli()
	}
	if err := saveMutes(state, m); err != nil {
		return err
	}
	fmt.Println(describeMute(key, m[key]))
	return nil
}

func muteKey(configPath, mxid string) (string, error) {
	if mxid == "" {
		return allMuted, nil
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return "", err
	}
	if _, ok := cfg.Entities[mxid]; !ok {
		return "", notHere(cfg, mxid)
	}
	return mxid, nil
}

func cmdUnmute(configPath, state string, args []string) error {
	fs := flag.NewFlagSet("unmute", flag.ExitOnError)
	parse(fs, args)
	if fs.NArg() > 1 {
		return usageError(fs.Name(), "unexpected %q", fs.Arg(1))
	}
	key, err := muteKey(configPath, fs.Arg(0))
	if err != nil {
		return err
	}
	m := loadMutes(state)
	if key == allMuted {
		m = Mutes{}
	} else {
		delete(m, key)
		if m.active(allMuted, time.Now()) {
			fmt.Fprintln(os.Stderr, "everyone is still muted (unmute with no MXID)")
		}
	}
	if err := saveMutes(state, m); err != nil {
		return err
	}
	wake(configPath)
	return nil
}

// Connecting is enough: socket activation starts the receiver, and it catches up.
func wake(configPath string) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return
	}
	addr, err := listenAddr(cfg)
	if err != nil {
		return
	}
	if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
		c.Close()
	}
}

func describeMute(key string, until int64) string {
	who := key
	if key == allMuted {
		who = "everyone"
	}
	if until == 0 {
		return who + " muted until unmute"
	}
	return who + " muted until " + time.UnixMilli(until).Format("2006-01-02 15:04")
}

func cmdStatus(configPath, state string, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}
	now := time.Now()
	m := loadMutes(state)
	keys := make([]string, 0, len(m))
	for k := range m {
		if m.active(k, now) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		fmt.Println("not muted")
	}
	for _, k := range keys {
		fmt.Println(describeMute(k, m[k]))
	}

	pending := listPending(state)
	if cfg, err := loadConfig(configPath); err == nil {
		for mxid := range cfg.Entities {
			if _, ok := pending[mxid]; !ok {
				pending[mxid] = nil
			}
		}
	}
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("\n%s\n", id)
		if len(pending[id]) == 0 {
			fmt.Println("  nothing pending")
		}
		for _, p := range pending[id] {
			line := fmt.Sprintf("  %s: %d unread from %s", p.Room, p.Unread, p.Sender)
			switch {
			case p.NextAt > 0:
				line += fmt.Sprintf(", retry %d at %s", p.Attempts+1, time.UnixMilli(p.NextAt).Format("15:04"))
			case p.DeliveredAt > 0:
				line += ", delivered " + time.UnixMilli(p.DeliveredAt).Format("2006-01-02 15:04")
			default:
				line += ", not delivered yet"
			}
			fmt.Println(line)
		}
	}

	dir, err := runtimeDir()
	if err != nil {
		return err
	}
	socks, _ := filepath.Glob(filepath.Join(dir, "*.sock"))
	fmt.Printf("\nsessions listening: %d\n", len(socks))
	for _, s := range socks {
		fmt.Println("  " + strings.TrimSuffix(filepath.Base(s), ".sock"))
	}
	return nil
}
