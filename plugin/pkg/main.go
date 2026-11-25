package main

import (
	"os"

	"github.com/grafana/grafana-plugin-sdk-go/backend/app"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"

	"github.com/oriraz/grafana-snow-integration/plugin/pkg/plugin"
)

func main() {
	if err := app.Manage("noc-alert-manager-app", plugin.NewApp, app.ManageOpts{}); err != nil {
		log.DefaultLogger.Error("Failed to manage plugin", "error", err)
		os.Exit(1)
	}
}
