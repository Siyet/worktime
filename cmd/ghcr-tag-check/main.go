package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Siyet/worktime/internal/releaseguard"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	image := os.Getenv("IMAGE")
	const imagePrefix = "ghcr.io/"
	if !strings.HasPrefix(image, imagePrefix) {
		return errors.New("IMAGE must be hosted on ghcr.io")
	}
	checker := releaseguard.GHCRChecker{
		Origin: "https://ghcr.io",
		Client: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: releaseguard.CheckGHCRBlobRedirect,
		},
	}
	repository := strings.TrimPrefix(image, imagePrefix)
	actor := os.Getenv("GITHUB_ACTOR")
	token := os.Getenv("GH_TOKEN")
	version := os.Getenv("VERSION")
	expectedDigest := os.Getenv("EXPECTED_IMAGE_DIGEST")
	if expectedDigest != "" {
		if err := checker.VerifyReusableTag(ctx, actor, token, repository, version, expectedDigest, os.Getenv("SOURCE_SHA")); err != nil {
			return fmt.Errorf("verify reusable container image %s@%s: %w", image, expectedDigest, err)
		}
		fmt.Printf("reusable container image %s@%s is unchanged and matches release metadata\n", image, expectedDigest)
		return nil
	}

	resolution, err := checker.ResolveTag(ctx, actor, token, repository, version)
	if err != nil {
		return fmt.Errorf("resolve container image %s:%s: %w", image, version, err)
	}
	if err := writeGitHubOutput(os.Getenv("GITHUB_OUTPUT"), resolution); err != nil {
		return err
	}
	if resolution.Exists {
		fmt.Printf("container image %s:%s resolves to %s and requires signature verification\n", image, version, resolution.Digest)
	} else {
		fmt.Printf("container image %s:%s is absent\n", image, version)
	}
	return nil
}

func writeGitHubOutput(filename string, resolution releaseguard.TagResolution) error {
	if filename == "" {
		return errors.New("GITHUB_OUTPUT is required")
	}
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open GITHUB_OUTPUT: %w", err)
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "exists=%t\ndigest=%s\n", resolution.Exists, resolution.Digest); err != nil {
		return fmt.Errorf("write GITHUB_OUTPUT: %w", err)
	}
	return nil
}
