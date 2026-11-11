package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Mode 600: it holds the webhook secrets.
type Config struct {
	URL      string             `json:"url"`
	Listen   string             `json:"listen,omitempty"`
	Entities map[string]*Entity `json:"entities"` // by Matrix ID
}

type Entity struct {
	TokenFile string   `json:"token_file"`
	OnNotice  []string `json:"on_notice,omitempty"`
	Secret    string   `json:"secret,omitempty"`
}

func defaultConfigPath() string {
	if p := os.Getenv("MATRIX_FOR_AI_CLI_CONFIG"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "matrix-for-ai-cli", "config.json")
}

func loadConfig(path string) (*Config, error) {
	cfg := &Config{Entities: map[string]*Entity{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fail(errConfig, "%s: %w", path, err)
	}
	if cfg.Entities == nil {
		cfg.Entities = map[string]*Entity{}
	}
	return cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return env, nil
}

func appserviceCredentials(e *Entity) (base, token, mxid string, err error) {
	env, err := readEnvFile(e.TokenFile)
	if err != nil {
		return "", "", "", fail(errTokenFile, "%w", err)
	}
	base, token, mxid = env["MATRIX_AI_BASE"], env["MATRIX_AI_TOKEN"], env["MATRIX_AI_MXID"]
	if base == "" || token == "" {
		return "", "", "", fail(errTokenFile, "%s: MATRIX_AI_BASE or MATRIX_AI_TOKEN missing", e.TokenFile)
	}
	return strings.TrimRight(base, "/"), token, mxid, nil
}

func (e *Entity) hasWebhook() bool { return e.Secret != "" && len(e.OnNotice) > 0 }
