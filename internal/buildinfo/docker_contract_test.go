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
	webEnd := strings.Index(dockerfile, "FROM --platform=$BUILDPLATFORM golang:")
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

func TestDockerBuildStagesCrossCompileNativelyForEachTarget(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(data)
	webStart := strings.Index(dockerfile, "FROM --platform=$BUILDPLATFORM node:22-alpine AS web")
	buildStart := strings.Index(dockerfile, "FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build")
	finalStart := strings.Index(dockerfile, "FROM scratch")
	if webStart < 0 || buildStart <= webStart || finalStart <= buildStart {
		t.Fatalf("Docker stages do not use the expected build and target platforms: web=%d build=%d final=%d", webStart, buildStart, finalStart)
	}
	buildStage := dockerfile[buildStart:finalStart]
	targetOS := strings.Index(buildStage, "ARG TARGETOS\n")
	targetArch := strings.Index(buildStage, "ARG TARGETARCH\n")
	crossCompile := strings.Index(buildStage, "CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build")
	if targetOS < 0 || targetArch < 0 || crossCompile < 0 || targetOS >= crossCompile || targetArch >= crossCompile {
		t.Fatalf("target arguments must precede the pure-Go cross-compile: os=%d arch=%d build=%d", targetOS, targetArch, crossCompile)
	}
	if strings.Contains(dockerfile[finalStart:], "$BUILDPLATFORM") {
		t.Fatal("final scratch image must retain the requested target platform")
	}
}

func TestDockerContextExcludesReleaseArtifacts(t *testing.T) {
	data, err := os.ReadFile("../../.dockerignore")
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "dist" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("release artifacts must be excluded from the Docker build context")
	}
}
