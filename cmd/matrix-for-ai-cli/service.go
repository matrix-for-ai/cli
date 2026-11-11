package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const serviceName = "matrix-for-ai-cli"

const serviceSubcommands = "install, uninstall, start or stop"

func cmdService(configPath string, args []string) error {
	if len(args) == 0 {
		return usageError("service", "missing subcommand: %s", serviceSubcommands)
	}
	switch args[0] {
	case "install", "uninstall", "start", "stop":
	default:
		return usageError("service", "unknown subcommand %q: %s", args[0], serviceSubcommands)
	}
	fs := flag.NewFlagSet("service "+args[0], flag.ExitOnError)
	wantedBy := fs.String("wanted-by", "", "")
	parse(fs, args[1:])
	if err := need(fs); err != nil {
		return err
	}
	if *wantedBy != "" && (args[0] != "install" || runtime.GOOS != "linux") {
		return usageError(fs.Name(), "--wanted-by is only for install, on Linux")
	}
	exe, err := clientPath()
	if err != nil {
		return err
	}
	var sv service
	switch runtime.GOOS {
	case "linux":
		sv = systemdService{exe, configPath, *wantedBy}
	case "darwin":
		sv = launchdService{exe, configPath}
	case "windows":
		sv = windowsService{exe, configPath}
	default:
		return fail(errUnsupported, "service: not on %s; run serve with your system's tools", runtime.GOOS)
	}
	switch args[0] {
	case "install":
		return sv.install()
	case "uninstall":
		return sv.uninstall()
	case "start":
		return sv.start()
	}
	return sv.stop()
}

type service interface {
	install() error
	uninstall() error
	start() error
	stop() error
}

// As called, not os.Executable: a symlink stays a symlink, so upgrading it upgrades the service.
func clientPath() (string, error) {
	p := os.Args[0]
	if !strings.ContainsRune(p, os.PathSeparator) && !strings.ContainsRune(p, '/') {
		var err error
		if p, err = exec.LookPath(p); err != nil {
			return "", err
		}
	}
	return filepath.Abs(p)
}

func servicePATH(exe string) string {
	dir, path := filepath.Dir(exe), os.Getenv("PATH")
	for _, p := range filepath.SplitList(path) {
		if p == dir {
			return path
		}
	}
	return dir + string(os.PathListSeparator) + path
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fail(errSystem, "%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// Removed first, so a symlink that was there is not written through.
func writeFresh(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	os.Remove(path)
	return os.WriteFile(path, []byte(content), 0o644)
}

type systemdService struct{ exe, config, wantedBy string }

func (s systemdService) dir() string {
	d := os.Getenv("XDG_CONFIG_HOME")
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, ".config")
	}
	return filepath.Join(d, "systemd", "user")
}

func (s systemdService) units() (socket, svc string, err error) {
	cfg, err := loadConfig(s.config)
	if err != nil {
		return "", "", err
	}
	addr, err := listenAddr(cfg)
	if err != nil {
		return "", "", err
	}
	esc := func(v string) string { return strconv.Quote(strings.ReplaceAll(v, "%", "%%")) }
	sockTarget, svcTarget, bound := "sockets.target", "default.target", ""
	if t := s.wantedBy; t != "" && t != "default.target" {
		sockTarget, svcTarget, bound = t, t, "PartOf="+t+"\nAfter="+t+"\n"
	}
	socket = fmt.Sprintf(`[Unit]
Description=%[1]s (socket)
%[3]s
[Socket]
ListenStream=%[2]s
# Listen even if the address is not up yet.
FreeBind=true

[Install]
WantedBy=%[4]s
`, serviceName, addr, bound, sockTarget)
	svc = fmt.Sprintf(`[Unit]
Description=%[1]s (delivers matrix-for-ai notices)
Requires=%[1]s.socket
After=%[1]s.socket
%[5]s
[Service]
ExecStart=%[2]s --config %[3]s serve --idle 15m
Environment=%[4]s

# Also when the target starts: catch up with what arrived meanwhile, then exit when idle.
[Install]
WantedBy=%[6]s
`, serviceName, esc(s.exe), esc(s.config), esc("PATH="+servicePATH(s.exe)), bound, svcTarget)
	return socket, svc, nil
}

func (s systemdService) install() error {
	socket, svc, err := s.units()
	if err != nil {
		return err
	}
	// What an earlier install enabled, under its own target, goes first.
	if _, err := os.Stat(filepath.Join(s.dir(), serviceName+".service")); err == nil {
		run("systemctl", "--user", "disable", serviceName+".service", serviceName+".socket")
	}
	if err := writeFresh(filepath.Join(s.dir(), serviceName+".socket"), socket); err != nil {
		return err
	}
	if err := writeFresh(filepath.Join(s.dir(), serviceName+".service"), svc); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "enable", serviceName+".socket", serviceName+".service"); err != nil {
		return err
	}
	// A running one keeps its old socket and binary until restarted.
	run("systemctl", "--user", "stop", serviceName+".service", serviceName+".socket")
	return s.start()
}

func (s systemdService) uninstall() error {
	run("systemctl", "--user", "disable", "--now", serviceName+".service", serviceName+".socket")
	for _, ext := range []string{".service", ".socket"} {
		if err := os.Remove(filepath.Join(s.dir(), serviceName+ext)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return run("systemctl", "--user", "daemon-reload")
}

func (s systemdService) start() error {
	return run("systemctl", "--user", "start", serviceName+".socket", serviceName+".service")
}

// stop stops the socket too: otherwise the next notice starts it again.
func (s systemdService) stop() error {
	return run("systemctl", "--user", "stop", serviceName+".service", serviceName+".socket")
}

type launchdService struct{ exe, config string }

func (s launchdService) plist() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceName+".plist")
}

func (s launchdService) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func (s launchdService) install() error {
	home, _ := os.UserHomeDir()
	x := func(v string) string {
		var b strings.Builder
		xml.EscapeText(&b, []byte(v))
		return b.String()
	}
	p := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>--config</string><string>%s</string><string>serve</string></array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>%s</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, serviceName, x(s.exe), x(s.config), x(servicePATH(s.exe)),
		x(filepath.Join(home, "Library", "Logs", serviceName+".log")))
	s.stop()
	if err := writeFresh(s.plist(), p); err != nil {
		return err
	}
	return s.start()
}

func (s launchdService) uninstall() error {
	s.stop()
	if err := os.Remove(s.plist()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s launchdService) start() error { return run("launchctl", "bootstrap", s.domain(), s.plist()) }

func (s launchdService) stop() error {
	return run("launchctl", "bootout", s.domain()+"/"+serviceName)
}

type windowsService struct{ exe, config string }

const runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

func (s windowsService) install() error {
	cmd := fmt.Sprintf(`"%s" --config "%s" service start`, s.exe, s.config)
	if err := run("reg", "add", runKey, "/v", serviceName, "/t", "REG_SZ", "/d", cmd, "/f"); err != nil {
		return err
	}
	s.stop()
	return s.start()
}

func (s windowsService) uninstall() error {
	s.stop()
	return run("reg", "delete", runKey, "/v", serviceName, "/f")
}

func (s windowsService) start() error {
	cmd := exec.Command(s.exe, "--config", s.config, "serve")
	cmd.Env = append(os.Environ(), "PATH="+servicePATH(s.exe))
	hide(cmd)
	return cmd.Start()
}

// stop ends the serve process only, not the client's other commands (listen, deliver…).
func (s windowsService) stop() error {
	ps := fmt.Sprintf(`Get-CimInstance Win32_Process -Filter "Name='%s'" | `+
		`Where-Object { $_.CommandLine -match ' serve$' } | ForEach-Object { Stop-Process -Id $_.ProcessId }`,
		filepath.Base(s.exe))
	return run("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
}
