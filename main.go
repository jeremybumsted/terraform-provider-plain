package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/jeremybumsted/terraform-provider-plain/plain"
)

// version is set at build time by GoReleaser.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), plain.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/jeremybumsted/plain",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
