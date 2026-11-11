package main

import (
	"log"
	"os/exec"
	"runtime"
	"strings"
)

func desktopNotify(title, body string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("osascript", "-e", "on run argv", "-e",
			"display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body)
	case "windows":
		ps := `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$x = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$t = $x.GetElementsByTagName('text'); $t.Item(0).InnerText = $env:N_TITLE; $t.Item(1).InnerText = $env:N_BODY
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('matrix-for-ai').Show([Windows.UI.Notifications.ToastNotification]::new($x))`
		cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
		cmd.Env = append(cmd.Environ(), "N_TITLE="+title, "N_BODY="+body)
	default:
		cmd = exec.Command("notify-send", "--app-name=matrix-for-ai", title, body)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("desktop notification: %v %s", err, strings.TrimSpace(string(out)))
	}
}
