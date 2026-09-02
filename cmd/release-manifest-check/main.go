package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/CapedHero/brigsby/internal/release"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("release-manifest-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "path to the release manifest")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *manifestPath == "" {
		fmt.Fprintln(stderr, "--manifest is required")
		return 2
	}

	manifest, err := release.Load(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "release manifest valid: %d export entries\n", len(manifest.Export.Allow))
	return 0
}
