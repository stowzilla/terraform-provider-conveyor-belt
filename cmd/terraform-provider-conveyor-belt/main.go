// cmd/terraform-provider-conveyor-belt/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"terraform-provider-conveyor-belt/internal/embedded"
	"terraform-provider-conveyor-belt/internal/provider"
)

// Generated documentation. This should be replaced with actual generation if needed.
const docGen = false

func main() {
	// Handle CLI subcommands before Terraform plugin protocol takes over.
	// This lets consumer scripts use the installed binary to locate DSL scripts:
	//   terraform-provider-conveyor-belt dsl-path
	if len(os.Args) > 1 && os.Args[1] == "dsl-path" {
		dir, err := embedded.ExtractScripts()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to extract embedded scripts: %s\n", err)
			os.Exit(1)
		}
		fmt.Println(embedded.ScriptPath(dir, ""))
		os.Exit(0)
	}

	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/<namespace>/dispatcher",
		Debug:   debug,
	}

	err := providerserver.Serve(context.Background(), provider.New, opts)
	if err != nil {
		log.Fatal(err.Error())
	}
}