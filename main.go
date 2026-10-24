// Command terraform-provider-deed is the Terraform provider for Deed, a declarative infrastructure catalog.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/aurimasniekis/terraform-provider-deed/internal/provider"
)

// version is set by the release build (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/aurimasniekis/deed",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
