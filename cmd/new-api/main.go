package main

import (
	"os"

	"github.com/QuantumNous/new-api/internal/app"
	"github.com/QuantumNous/new-api/internal/transport/http/routes"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "plugin" {
		os.Exit(jsplugin.RunCLI(os.Args[2:], os.Stdout, os.Stderr))
	}
	app.Run(router.WebAssets{BuildFS: web.Files, IndexPage: web.IndexPage})
}
