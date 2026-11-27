package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deploy/versions.json is hashed into every backup's tool-versions digest, so a
// dependency bump that leaves it stale deploys code the manifest does not
// describe. This keeps the manifest's module pins equal to go.mod.
func TestVersionsManifestMatchesGoModule(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var manifest struct {
		Go    string `json:"go"`
		Graph struct {
			Driver string `json:"driver"`
		} `json:"graph"`
		Echo           string `json:"echo"`
		Generator      string `json:"generator"`
		Validator      string `json:"validator"`
		Testcontainers string `json:"testcontainers"`
		Testify        string `json:"testify"`
		Linter         string `json:"linter"`
		Encryption     string `json:"encryption"`
		Otel           string `json:"otel"`
		BuildImage     string `json:"build_image"`
	}
	require.NoError(t, json.Unmarshal(raw, &manifest))
	gomod, err := os.ReadFile("../../go.mod")
	require.NoError(t, err)
	required := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*(?:require\s+)?([a-z0-9][^\s]*\.[^\s]+)\s+(v[^\s]+)`).FindAllStringSubmatch(string(gomod), -1) {
		required[m[1]] = m[2]
	}
	goDirective := regexp.MustCompile(`(?m)^go\s+(\S+)`).FindStringSubmatch(string(gomod))
	require.Len(t, goDirective, 2)
	assert.Equal(t, goDirective[1], manifest.Go, "go directive")
	assert.True(t, strings.HasPrefix(manifest.BuildImage, "golang:"+manifest.Go+"-"), "build image %s does not use Go %s", manifest.BuildImage, manifest.Go)
	for _, pin := range []string{manifest.Graph.Driver, manifest.Echo, manifest.Generator, manifest.Validator, manifest.Testcontainers, manifest.Testify, manifest.Linter, manifest.Encryption, manifest.Otel} {
		module, version, ok := strings.Cut(pin, "@")
		require.True(t, ok, "pin %q is not module@version", pin)
		assert.Equal(t, version, required[module], "deploy/versions.json pins %s; go.mod requires %q", pin, required[module])
	}
}

// Dependabot also bumps the base images in deploy/Dockerfile.*. Each image a
// Dockerfile builds from must be the one the manifest records, so an image bump
// fails here until deploy/versions.json moves with it.
func TestVersionsManifestMatchesDockerfiles(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var manifest struct {
		Graph struct {
			Image string `json:"image"`
		} `json:"graph"`
		BuildImage   string `json:"build_image"`
		RuntimeImage string `json:"api_worker_runtime_image"`
	}
	require.NoError(t, json.Unmarshal(raw, &manifest))
	recorded := map[string]bool{manifest.Graph.Image: true, manifest.BuildImage: true, manifest.RuntimeImage: true}
	from := regexp.MustCompile(`(?m)^FROM\s+(?:--platform=\S+\s+)?(\S+)`)
	for _, name := range []string{"Dockerfile.api", "Dockerfile.worker", "Dockerfile.graph", "Dockerfile.fixture-proxy"} {
		dockerfile, err := os.ReadFile("../../../deploy/" + name)
		require.NoError(t, err)
		images := from.FindAllStringSubmatch(string(dockerfile), -1)
		require.NotEmpty(t, images, name)
		for _, m := range images {
			assert.True(t, recorded[m[1]], "%s builds from %s, which deploy/versions.json does not record", name, m[1])
		}
	}
}
