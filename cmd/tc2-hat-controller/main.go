package main

import (
	"fmt"
	"os"

	"github.com/TheCacophonyProject/go-utils/logging"
	serialhelper "github.com/TheCacophonyProject/tc2-hat-controller/internal/serial-helper"
	attiny "github.com/TheCacophonyProject/tc2-hat-controller/internal/tc2-hat-attiny"
)

var (
	// These variables are set by environment variables. GithubActions will set them automatically.
	// The are needed for testing though so the values can be set as shown below.
	attinyMajorStr = "" // To set for testing run `export ATTINY_MAJOR=1`
	attinyMinorStr = "" // To set for testing run `export ATTINY_MINOR=0`
	attinyPatchStr = "" // To set for testing run `export ATTINY_PATCH=0`
	attinyHexHash  = "" // To set for testing run `export ATTINY_HASH=$(sha256sum _release/attiny-firmware.hex | cut -d ' ' -f 1)`
)

var log *logging.Logger

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err)
	}
}

var version = "<not set>"

func runMain() error {
	log = logging.NewLogger("info")
	if len(os.Args) < 2 {
		log.Info("Usage: tool <subcommand> [args]")
		return fmt.Errorf("no subcommand given")
	}

	subcommand := os.Args[1]
	args := os.Args[2:]

	var err error
	switch subcommand {
	case "serial-helper":
		err = serialhelper.Run(args, version)
	case "attiny":
		err = attiny.Run(args, version, attinyMajorStr, attinyMinorStr, attinyPatchStr, attinyHexHash)
	default:
		err = fmt.Errorf("unknown subcommand: %s", subcommand)
	}

	return err
}
