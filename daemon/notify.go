package main

import (
	"os/exec"

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
		"-h", "string:x-omarchy-omamessages:"+core.JoinID(n.Provider, n.ConvID), title, n.Body)
	if err := cmd.Run(); err != nil {
		logger.Debug().Err(err).Msg("notify-send failed")
	}
}
