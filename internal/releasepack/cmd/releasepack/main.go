// Package main provides the releasepack command-line entry point.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/releasepack"
)

var errInvalidUsage = errors.New("invalid usage")

type requiredFlag struct {
	value string
	count int
}

func (value *requiredFlag) String() string { return value.value }
func (value *requiredFlag) Set(text string) error {
	value.count++
	value.value = text
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	err := dispatch(args)
	if err == nil {
		return 0
	}
	category := "invalid_usage"
	if !errors.Is(err, errInvalidUsage) {
		category = releasepack.ErrorCategory(err)
	}
	_, _ = fmt.Fprintf(stderr, "releasepack: %s\n", category)
	return 1
}

func dispatch(args []string) error {
	if len(args) == 0 {
		return errInvalidUsage
	}
	for _, argument := range args[1:] {
		if argument == "--" || (strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--")) {
			return errInvalidUsage
		}
	}
	switch args[0] {
	case "archives":
		return runArchives(args[1:])
	case "sbom":
		return runSBOM(args[1:])
	case "checksums":
		return runChecksums(args[1:])
	default:
		return errInvalidUsage
	}
}

func runArchives(args []string) error {
	set := newFlagSet("archives")
	repositoryRoot, stagingRoot, outputRoot, tag := commonFlags(set)
	var sourceEpoch requiredFlag
	set.Var(&sourceEpoch, "source-epoch", "")
	if err := parseExact(set, args, repositoryRoot, stagingRoot, outputRoot, tag, &sourceEpoch); err != nil {
		return err
	}
	sourceTime, err := parseSourceEpoch(sourceEpoch.value)
	if err != nil {
		return errInvalidUsage
	}
	_, err = releasepack.WriteArchives(releasepack.ArchiveOptions{
		RepositoryRoot: repositoryRoot.value,
		StagingRoot:    stagingRoot.value,
		OutputRoot:     outputRoot.value,
		Tag:            tag.value,
		SourceTime:     sourceTime,
	})
	return err
}

func runSBOM(args []string) error {
	set := newFlagSet("sbom")
	repositoryRoot, stagingRoot, outputRoot, tag := commonFlags(set)
	var rawPath, sourceEpoch requiredFlag
	set.Var(&rawPath, "raw-sbom", "")
	set.Var(&sourceEpoch, "source-epoch", "")
	if err := parseExact(set, args, repositoryRoot, stagingRoot, outputRoot, tag, &rawPath, &sourceEpoch); err != nil {
		return err
	}
	sourceTime, err := parseSourceEpoch(sourceEpoch.value)
	if err != nil {
		return errInvalidUsage
	}
	_, err = releasepack.WriteSBOM(releasepack.SBOMOptions{
		RepositoryRoot: repositoryRoot.value,
		StagingRoot:    stagingRoot.value,
		OutputRoot:     outputRoot.value,
		RawPath:        rawPath.value,
		Tag:            tag.value,
		SourceTime:     sourceTime,
	})
	return err
}

func runChecksums(args []string) error {
	set := newFlagSet("checksums")
	repositoryRoot, stagingRoot, outputRoot, tag := commonFlags(set)
	if err := parseExact(set, args, repositoryRoot, stagingRoot, outputRoot, tag); err != nil {
		return err
	}
	_, err := releasepack.WriteChecksums(releasepack.ChecksumOptions{
		RepositoryRoot: repositoryRoot.value,
		StagingRoot:    stagingRoot.value,
		OutputRoot:     outputRoot.value,
		Tag:            tag.value,
	})
	return err
}

func newFlagSet(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.Usage = func() {}
	return set
}

func commonFlags(set *flag.FlagSet) (*requiredFlag, *requiredFlag, *requiredFlag, *requiredFlag) {
	var repositoryRoot, stagingRoot, outputRoot, tag requiredFlag
	set.Var(&repositoryRoot, "repository-root", "")
	set.Var(&stagingRoot, "staging-root", "")
	set.Var(&outputRoot, "output-root", "")
	set.Var(&tag, "tag", "")
	return &repositoryRoot, &stagingRoot, &outputRoot, &tag
}

func parseExact(set *flag.FlagSet, args []string, values ...*requiredFlag) error {
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		return errInvalidUsage
	}
	for _, value := range values {
		if value.count != 1 || value.value == "" {
			return errInvalidUsage
		}
	}
	return nil
}

func parseSourceEpoch(value string) (time.Time, error) {
	epoch, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(epoch, 0).UTC(), nil
}
