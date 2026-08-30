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
			Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("registry redirects are not allowed")
			},
		},
	}
	err := checker.RequireTagAbsent(
		ctx,
		os.Getenv("GITHUB_ACTOR"),
		os.Getenv("GH_TOKEN"),
		strings.TrimPrefix(image, imagePrefix),
		os.Getenv("VERSION"),
	)
	if errors.Is(err, releaseguard.ErrTagExists) {
		return fmt.Errorf("container image %s:%s already exists; stopping before push", image, os.Getenv("VERSION"))
	}
	if err != nil {
		return fmt.Errorf("cannot prove that container image %s:%s is absent: %w", image, os.Getenv("VERSION"), err)
	}
	fmt.Printf("container image %s:%s is absent\n", image, os.Getenv("VERSION"))
	return nil
}
