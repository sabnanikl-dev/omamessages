package main

import (
	"os/exec"
	"strings"

	"omarchy-omamessages/core"
)

// SendNotification shows one desktop notification, under the service's name
// so each service can be told apart (and silenced) in the notification center.
// The hint carries the namespaced conversation ID.
func SendNotification(n core.Notification, appName string) {
	title := n.Title
	if title == "" {
		title = appName
	}
	cmd := exec.Command("notify-send", "-a", appName, "-i", "chat",
		"-h", "string:x-omarchy-omamessages:"+core.JoinID(n.Provider, n.ConvID), title, escapeMarkup(n.Body))
	if err := cmd.Run(); err != nil {
		logger.Debug().Err(err).Msg("notify-send failed")
	}
}

// escapeMarkup keeps a notification body literal. The body is message text
// from other people, and notification servers (Omarchy's included) render
// markup and links in bodies.
func escapeMarkup(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
