// Package service installs routsi as a keep-alive background service — a
// launchd agent on macOS, a systemd --user unit on Linux. Both restart routsi
// on crash and start it at login, so it stays up (the watchdog the user
// wants). No root: everything lives under the user's own home.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const label = "com.routsi"

// Params describe what to run.
type Params struct {
	Binary string // absolute path to the routsi binary
	Config string // absolute path to models.yaml ("" = let routsi resolve)
	Listen string // listen address for logging/hint only
}

// Install writes and loads the service. Returns a human-readable summary.
func Install(p Params) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(p)
	case "linux":
		return installSystemd(p)
	default:
		return "", fmt.Errorf("service install unsupported on %s (run `routsi serve` directly)", runtime.GOOS)
	}
}

// Uninstall stops and removes the service.
func Uninstall() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		path := launchdPath()
		_ = exec.Command("launchctl", "unload", path).Run()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return "removed launchd agent " + path, nil
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", "routsi").Run()
		path := systemdPath()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return "removed systemd unit " + path, nil
	default:
		return "", fmt.Errorf("unsupported on %s", runtime.GOOS)
	}
}

// Status reports whether the service is loaded/running.
func Status() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "list").Output()
		if err != nil {
			return "", err
		}
		if strings.Contains(string(out), label) {
			return "running (launchd: " + label + ")", nil
		}
		return "not loaded", nil
	case "linux":
		out, _ := exec.Command("systemctl", "--user", "is-active", "routsi").Output()
		return "systemd routsi: " + strings.TrimSpace(string(out)), nil
	default:
		return "", fmt.Errorf("unsupported on %s", runtime.GOOS)
	}
}

func launchdPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func systemdPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", "routsi.service")
}

func installLaunchd(p Params) (string, error) {
	args := "<string>serve</string>"
	if p.Config != "" {
		args += "\n      <string>-config</string>\n      <string>" + p.Config + "</string>"
	}
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, "Library", "Logs")
	_ = os.MkdirAll(logDir, 0o755)
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key>
  <array>
      <string>` + p.Binary + `</string>
      ` + args + `
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>` + filepath.Join(logDir, "routsi.log") + `</string>
  <key>StandardErrorPath</key><string>` + filepath.Join(logDir, "routsi.log") + `</string>
</dict>
</plist>
`
	path := launchdPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return "", err
	}
	_ = exec.Command("launchctl", "unload", path).Run() // idempotent reload
	if err := exec.Command("launchctl", "load", path).Run(); err != nil {
		return "", fmt.Errorf("launchctl load: %w", err)
	}
	return "installed & started launchd agent " + path + "\n  logs: " + filepath.Join(logDir, "routsi.log"), nil
}

func installSystemd(p Params) (string, error) {
	execLine := p.Binary + " serve"
	if p.Config != "" {
		execLine += " -config " + p.Config
	}
	unit := `[Unit]
Description=routsi — OpenAI-compatible model/agent router
After=network-online.target

[Service]
ExecStart=` + execLine + `
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`
	path := systemdPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return "", err
	}
	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return "", fmt.Errorf("daemon-reload: %w", err)
	}
	if err := exec.Command("systemctl", "--user", "enable", "--now", "routsi").Run(); err != nil {
		return "", fmt.Errorf("systemctl enable: %w", err)
	}
	return "installed & started systemd unit " + path + "\n  hint: `loginctl enable-linger` to keep it running after logout", nil
}

