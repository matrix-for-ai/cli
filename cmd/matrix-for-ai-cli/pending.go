package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
)

type Pending struct {
	Room        string `json:"room"`
	Sender      string `json:"sender"`
	Unread      int    `json:"unread"`
	DeliveredAt int64  `json:"delivered_at,omitempty"` // unix ms
	Attempts    int    `json:"attempts,omitempty"`     // failed, since the last delivery
	NextAt      int64  `json:"next_at,omitempty"`      // unix ms of the next retry; 0: none
}

func defaultStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "matrix-for-ai-cli")
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "matrix-for-ai-cli", "state")
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "matrix-for-ai-cli")
	}
	return "state"
}

// Matrix IDs carry ':', which Windows does not allow in names.
func pendingDir(state, mxid string) string {
	return filepath.Join(state, "pending", url.QueryEscape(mxid))
}

func savePending(state, mxid string, p Pending) error {
	dir := pendingDir(state, mxid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".p-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, url.QueryEscape(p.Room)+".json"))
}

func removePending(state, mxid, room string) {
	os.Remove(filepath.Join(pendingDir(state, mxid), url.QueryEscape(room)+".json"))
}

func listPending(state string) map[string][]Pending {
	out := map[string][]Pending{}
	ents, _ := os.ReadDir(filepath.Join(state, "pending"))
	for _, ent := range ents {
		mxid, err := url.QueryUnescape(ent.Name())
		if err != nil || !ent.IsDir() {
			continue
		}
		dir := filepath.Join(state, "pending", ent.Name())
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, f := range files {
			var p Pending
			if data, err := os.ReadFile(f); err == nil && json.Unmarshal(data, &p) == nil && p.Room != "" {
				out[mxid] = append(out[mxid], p)
			}
		}
	}
	return out
}
