package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/docker/sandbox-kit-spec/v3/internal/capabilitycatalog"
)

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("expected release or docs subcommand")
	}
	switch args[0] {
	case "release":
		flags := flag.NewFlagSet("release", flag.ContinueOnError)
		root := flags.String("root", ".", "specification repository root")
		version := flags.String("version", "", "release tag")
		revision := flags.String("revision", "", "full commit revision")
		previousRef := flags.String("previous-ref", "", "previous release tag")
		outputDir := flags.String("output-dir", "", "output directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *outputDir == "" {
			return errors.New("release requires --output-dir and no positional arguments")
		}
		current, err := capabilitycatalog.Build(*root, *version, *revision)
		if err != nil {
			return err
		}
		var previous *capabilitycatalog.Catalog
		if *previousRef != "" {
			old, err := capabilitycatalog.Previous(*root, *previousRef)
			if err != nil {
				return err
			}
			previous = &old
		}
		return capabilitycatalog.WriteRelease(*outputDir, current, previous)
	case "docs":
		flags := flag.NewFlagSet("docs", flag.ContinueOnError)
		catalog := flags.String("catalog", "", "release catalog JSON")
		page := flags.String("page", "", "Docker docs capability support page")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *catalog == "" || *page == "" {
			return errors.New("docs requires --catalog and --page")
		}
		c, err := capabilitycatalog.ReadCatalog(*catalog)
		if err != nil {
			return err
		}
		return capabilitycatalog.WritePage(*page, c)
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "capability-catalog:", err)
		os.Exit(1)
	}
}
