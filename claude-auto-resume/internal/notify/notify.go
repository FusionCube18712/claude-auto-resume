// Package notify sends a best-effort desktop notification and rings the
// terminal bell when a limit is hit or a session resumes. Failures are silently
// ignored — a notification is a nicety, never required.
package notify

import (
	"os"
	"os/exec"
	"runtime"
)

// Send rings the terminal bell and attempts a native desktop notification for
// the current OS. All steps are best-effort.
func Send(title, body string) {
	// Terminal bell — works everywhere a terminal is attached.
	os.Stderr.WriteString("\a")

	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("osascript"); err == nil {
			script := "display notification " + quote(body) + " with title " + quote(title)
			_ = exec.Command("osascript", "-e", script).Start()
		}
	case "linux":
		if _, err := exec.LookPath("notify-send"); err == nil {
			_ = exec.Command("notify-send", title, body).Start()
		}
	case "windows":
		// PowerShell balloon/toast. Best-effort; ignored if it fails.
		ps := `[reflection.assembly]::loadwithpartialname('System.Windows.Forms') > $null;` +
			`$n=New-Object System.Windows.Forms.NotifyIcon;` +
			`$n.Icon=[System.Drawing.SystemIcons]::Information;$n.Visible=$true;` +
			`$n.ShowBalloonTip(5000,` + psQuote(title) + `,` + psQuote(body) + `,'Info')`
		if _, err := exec.LookPath("powershell"); err == nil {
			_ = exec.Command("powershell", "-NoProfile", "-Command", ps).Start()
		}
	}
}

// quote wraps s in double quotes for AppleScript, escaping embedded quotes.
func quote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	out = append(out, '"')
	return string(out)
}

func psQuote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'') // PowerShell escapes ' by doubling
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}
