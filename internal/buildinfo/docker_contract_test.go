package buildinfo

import (
	"os"
	"strings"
	"testing"
)

func TestDockerFeedsOneVersionToFrontendAndServerBuilds(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(data)
	webEnd := strings.Index(dockerfile, "FROM golang:")
	if webEnd < 0 {
		t.Fatal("Dockerfile has no Go build stage")
	}
	webStage := dockerfile[:webEnd]
	argument := strings.Index(webStage, "ARG VERSION=dev")
	clientVersion := strings.Index(webStage, "ENV VITE_WORKTIME_VERSION=${VERSION}")
	webBuild := strings.Index(webStage, "RUN npm run build")
	if argument < 0 || clientVersion <= argument || webBuild <= clientVersion {
		t.Fatalf("Docker web version is not injected before its build: arg=%d env=%d build=%d", argument, clientVersion, webBuild)
	}
	if !strings.Contains(dockerfile[webEnd:], "buildinfo.Version=${VERSION}") {
		t.Fatal("Docker server build does not use the same VERSION argument")
	}
}
