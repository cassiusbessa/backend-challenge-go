package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wantGo is the toolchain the module, the image and the workflow share. A bump
// changes this line, and the test names the file that stayed behind.
const wantGo = "1.27.1"

func TestToolchainPinsOneGoVersionEverywhere(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mod := readText(t, filepath.Join(root, "go.mod"))
	if !strings.Contains(mod, "\ngo "+wantGo+"\n") {
		t.Fatalf("go.mod is missing %q", "\ngo "+wantGo+"\n")
	}
	docker := readText(t, filepath.Join(root, "Dockerfile"))
	if !strings.Contains(docker, "golang:"+wantGo) {
		t.Fatalf("Dockerfile is missing %q", "golang:"+wantGo)
	}
	ci := readText(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	if !strings.Contains(ci, `go-version: "`+wantGo+`"`) {
		t.Fatalf("ci.yml pins no toolchain: want %q", `go-version: "`+wantGo+`"`)
	}
}

// The integration suite needs a real token, so the IdP joins the step. Grafana
// stays out: nothing under test reads a dashboard.
func TestIntegrationWorkflowOmitsGrafana(t *testing.T) {
	t.Parallel()
	ci := readText(t, filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	line := composeLine(ci)
	if line == "" {
		t.Fatalf("ci.yml is missing %q", "docker compose up")
	}
	for _, want := range []string{"postgres", "localstack", "keycloak", "otel-collector"} {
		if !strings.Contains(line, want) {
			t.Fatalf("integration step is missing %s: %s", want, line)
		}
	}
	if strings.Contains(line, "grafana") {
		t.Fatalf("integration step includes grafana: %s", line)
	}
}

// The schema is applied by the migration step, before the suite. The process
// carries no migration code, so a workflow without this step would run the
// suite against an empty database.
func TestIntegrationWorkflowAppliesTheSchemaBeforeTheSuite(t *testing.T) {
	t.Parallel()
	ci := readText(t, filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	migration := strings.Index(ci, "docker compose run --rm migrate")
	if migration < 0 {
		t.Fatalf("ci.yml is missing %q", "docker compose run --rm migrate")
	}
	suite := strings.Index(ci, "-tags=integration")
	if suite < 0 {
		t.Fatalf("ci.yml is missing the integration suite")
	}
	if migration > suite {
		t.Fatalf("the migration step comes after the suite, want it before")
	}
}

func composeLine(workflow string) string {
	for _, line := range strings.Split(workflow, "\n") {
		if strings.Contains(line, "docker compose up") {
			return line
		}
	}
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Clean(filepath.Join("..", "..", ".."))
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // fixed path inside the test repository
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
