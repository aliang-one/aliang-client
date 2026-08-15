package setup

import (
	"bytes"
	"strings"
	"text/template"
)

// LaunchdPlistData macOS LaunchDaemon/LaunchAgent plist 模板数据
type LaunchdPlistData struct {
	Label            string
	ProgramPath      string
	Args             []string
	RunAtLoad        bool
	KeepAlive        bool
	StandardOutPath  string
	StandardErrorPath string
	WorkingDirectory string
	EnvironmentVars  map[string]string
}

// SystemdUnitData Linux systemd unit 模板数据
type SystemdUnitData struct {
	Description     string
	After           string
	Wants           string
	ExecStart       string
	RestartPolicy   string
	RestartSec      string
	User            string
	Group           string
	StandardOutput  string
	StandardError   string
	WantedBy        string
	EnvironmentVars map[string]string
}

// macOS LaunchDaemon/LaunchAgent Plist 模板
const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.ProgramPath}}</string>
		{{range .Args}}
		<string>{{.}}</string>
		{{end}}
	</array>
	<key>RunAtLoad</key>
	<{{.RunAtLoad}}/>
	<key>KeepAlive</key>
	<{{.KeepAlive}}/>
	{{if .WorkingDirectory}}
	<key>WorkingDirectory</key>
	<string>{{.WorkingDirectory}}</string>
	{{end}}
	{{if .StandardOutPath}}
	<key>StandardOutPath</key>
	<string>{{.StandardOutPath}}</string>
	{{end}}
	{{if .StandardErrorPath}}
	<key>StandardErrorPath</key>
	<string>{{.StandardErrorPath}}</string>
	{{end}}
	{{if .EnvironmentVars}}
	<key>EnvironmentVariables</key>
	<dict>
		{{range $key, $value := .EnvironmentVars}}
		<key>{{$key}}</key>
		<string>{{$value}}</string>
		{{end}}
	</dict>
	{{end}}
</dict>
</plist>
`

// Linux systemd unit 文件模板
const systemdUnitTemplate = `[Unit]
Description={{.Description}}
After={{.After}}
Wants={{.Wants}}

[Service]
Type=simple
{{if .User}}User={{.User}}{{end}}
{{if .Group}}Group={{.Group}}{{end}}
ExecStart={{.ExecStart}}
Restart={{.RestartPolicy}}
RestartSec={{.RestartSec}}
StandardOutput={{.StandardOutput}}
StandardError={{.StandardError}}
{{range $key, $value := .EnvironmentVars}}
Environment="{{$key}}={{$value}}"
{{end}}

[Install]
WantedBy={{.WantedBy}}
`

// RenderLaunchdPlist 渲染 macOS plist 模板
func RenderLaunchdPlist(data LaunchdPlistData) (string, error) {
	tmpl, err := template.New("launchd").Parse(launchdPlistTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// RenderSystemdUnit 渲染 Linux systemd unit 模板
func RenderSystemdUnit(data SystemdUnitData) (string, error) {
	tmpl, err := template.New("systemd").Parse(systemdUnitTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return strings.TrimSpace(buf.String()), nil
}