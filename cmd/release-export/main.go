// release-export stages a reviewed public release; it never performs Git writes.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/CapedHero/brigsby/internal/release"
)

const exporterVersion = "brigsby-release-export/v1"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("release-export", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "path to the reviewed release manifest")
	source := flags.String("source", "", "private source checkout")
	destination := flags.String("destination", "", "empty staging directory")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *manifestPath == "" || *source == "" || *destination == "" {
		fmt.Fprintln(stderr, "--manifest, --source, and --destination are required")
		return 2
	}
	manifest, err := release.Load(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	commit, err := cleanSourceCommit(*source, manifest)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report, err := release.Stage(*source, *destination, manifest)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "staged release: files=%d manifest=%s exporter=%s source=%s destination=%s\n", len(report.Files), report.ManifestDigest, exporterVersion, commit, *destination)
	for _, file := range report.Files {
		_, _ = fmt.Fprintf(stdout, "FILE %s\n", file)
	}
	return 0
}

func cleanSourceCommit(source string, manifest release.Manifest) (string, error) {
	status, err := exec.Command("git", "-C", source, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching").Output()
	if err != nil {
		return "", fmt.Errorf("release source must be a clean Git checkout")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(status)), "\n") {
		if len(line) < 4 || !isExportedPath(strings.TrimSpace(line[3:]), manifest) {
			continue
		}
		return "", fmt.Errorf("release source has uncommitted exportable content")
	}
	output, err := exec.Command("git", "-C", source, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("release source must have a committed HEAD")
	}
	return strings.TrimSpace(string(output)), nil
}

func isExportedPath(pathname string, manifest release.Manifest) bool {
	for _, allowed := range manifest.Export.Allow {
		if pathname == allowed || strings.HasPrefix(pathname, allowed+"/") {
			return true
		}
	}
	return false
}
