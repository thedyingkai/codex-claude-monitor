package agent

import (
	"fmt"
	"html"
	"path"
	"path/filepath"
	"strings"
)

type ServiceFileConfig struct {
	Executable        string
	ConfigPath        string
	FirmwareDirectory string
	WorkingDirectory  string
	WindowsUserID     string
	Mode              string
}

func serviceCommand(cfg ServiceFileConfig) (string, string, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = "agent"
	}
	switch mode {
	case "agent":
		if cfg.ConfigPath == "" {
			return "", "", fmt.Errorf("agent service requires a config path")
		}
		return "Codex and Claude quota monitor agent", "agent --config " + systemdEscape(cfg.ConfigPath), nil
	case "standalone":
		return "Codex and Claude standalone quota monitor", "standalone", nil
	default:
		return "", "", fmt.Errorf("unsupported service mode %q", cfg.Mode)
	}
}

// GenerateWindowsTaskXML returns a current-user, at-logon scheduled task. The
// caller writes/registers it, which keeps privilege-changing operations out of
// this package.
func GenerateWindowsTaskXML(cfg ServiceFileConfig) (string, error) {
	if cfg.Executable == "" {
		return "", fmt.Errorf("executable is required")
	}
	_, arguments, err := serviceCommand(cfg)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(arguments, "agent --config ") {
		arguments = `agent --config &quot;` + html.EscapeString(cfg.ConfigPath) + `&quot;`
	} else if strings.EqualFold(strings.TrimSpace(cfg.Mode), "standalone") && cfg.FirmwareDirectory != "" {
		arguments += ` --firmware-dir &quot;` + html.EscapeString(cfg.FirmwareDirectory) + `&quot;`
	}
	working := cfg.WorkingDirectory
	if working == "" {
		working = filepath.Dir(cfg.Executable)
	}
	principal := ""
	if cfg.WindowsUserID != "" {
		principal = "\n      <UserId>" + html.EscapeString(cfg.WindowsUserID) + "</UserId>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <LogonTrigger><Enabled>true</Enabled></LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">` + principal + `
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure><Interval>PT1M</Interval><Count>10</Count></RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + html.EscapeString(cfg.Executable) + `</Command>
      <Arguments>` + arguments + `</Arguments>
      <WorkingDirectory>` + html.EscapeString(working) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`, nil
}

// GenerateSystemdUnit returns a per-user unit so the process sees the same
// provider credential stores as the interactive CLI login.
func GenerateSystemdUnit(cfg ServiceFileConfig) (string, error) {
	if cfg.Executable == "" {
		return "", fmt.Errorf("executable is required")
	}
	description, arguments, err := serviceCommand(cfg)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Mode), "standalone") {
		if cfg.FirmwareDirectory == "" {
			// This is a user unit. Keep firmware alongside the user's other
			// quota-monitor state instead of falling back to root-owned /var/lib.
			arguments += ` --firmware-dir="%h/.local/share/quota-monitor/firmware"`
		} else {
			arguments += " --firmware-dir " + systemdEscape(cfg.FirmwareDirectory)
		}
	}
	working := cfg.WorkingDirectory
	if working == "" {
		// Units always use POSIX paths, even when generated on Windows.
		if cfg.ConfigPath != "" {
			working = path.Dir(cfg.ConfigPath)
		} else {
			working = path.Dir(cfg.Executable)
		}
	}
	return `[Unit]
Description=` + description + `
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=` + systemdEscape(working) + `
# Covers conventional per-user and system-wide CLI installs. nvm/asdf paths
# are versioned, so agent.json should use absolute codexCommand and
# claudeCommand values on those installations.
Environment="PATH=%h/.local/bin:%h/.local/share/pnpm:%h/.npm-global/bin:/usr/local/bin:/usr/bin:/bin"
ExecStart=` + systemdEscape(cfg.Executable) + ` ` + arguments + `
Restart=on-failure
RestartSec=15s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
# Provider CLIs and quota-monitor update per-user credential, cache, database,
# token, and firmware files. "strict" would make HOME read-only as part of the
# whole filesystem even with ProtectHome=false; "full" still protects
# /usr, /boot, and /etc while leaving the unprivileged user's state writable.
ProtectHome=false
UMask=0077

[Install]
WantedBy=default.target
`, nil
}

func systemdEscape(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(value) + `"`
}
