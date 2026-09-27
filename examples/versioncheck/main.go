//go:build ignore

// Legacy example from the upstream doctype/steam fork: it targets an older API and is excluded from the build.

package main

import (
	"log"

	"github.com/doctype/steam"
)

func main() {
	version, err := steam.NewSessionWithAPIKey("").GetRequiredSteamAppVersion(730)
	if err != nil {
		log.Fatal(err)
	}
	log.Print("Current CS:GO version is: ", version)
}
