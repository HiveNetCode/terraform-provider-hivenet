package main

import (
	"context"
	"flag"
	"log"

	"github.com/HiveNetCode/terraform-provider-hivenet/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// Set by GoReleaser at build time.
var version string = "dev"

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		// Namespace must match the lowercased GitHub org the registry publishes from.
		Address: "registry.terraform.io/hivenetcode/hivenet",
		Debug:   debug,
	}

	err := providerserver.Serve(context.Background(), provider.New(version), opts)

	if err != nil {
		log.Fatal(err.Error())
	}
}
