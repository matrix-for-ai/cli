package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Delivery IDs already taken, with when (unix ms): on disk, so a restart does not let a replayed
// notice in while its ts is still fresh.
type Seen map[string]int64

func seenPath(state string) string { return filepath.Join(state, "seen.json") }

func loadSeen(state string) Seen {
	s := Seen{}
	if data, err := os.ReadFile(seenPath(state)); err == nil {
		json.Unmarshal(data, &s)
	}
	return s
}

func saveSeen(state string, s Seen) error {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := seenPath(state) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, seenPath(state))
}

// Past twice the allowed skew, a notice is refused as stale anyway.
func (s Seen) prune(now time.Time) {
	for id, at := range s {
		if now.Sub(time.UnixMilli(at)) > 2*maxSkew {
			delete(s, id)
		}
	}
}
