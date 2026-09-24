package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolchainPinsGo1264(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	mod := readText(t, filepath.Join(root, "go.mod"))
	if !strings.Contains(mod, "\ngo 1.26.4\n") {
		t.Fatal("go.mod não fixa go 1.26.4")
	}
	docker := readText(t, filepath.Join(root, "Dockerfile"))
	if !strings.Contains(docker, "golang:1.26.4") {
		t.Fatal("Dockerfile não usa golang:1.26.4")
	}
	ci := readText(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	if !strings.Contains(ci, `go-version: "1.26.4"`) {
		t.Fatal("setup-go do CI não fixa 1.26.4")
	}
}

func TestIntegrationWorkflowOmitsKeycloakAndGrafana(t *testing.T) {
	t.Parallel()
	ci := readText(t, filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	line := composeLine(ci)
	if line == "" {
		t.Fatal("workflow sem docker compose up")
	}
	for _, want := range []string{"postgres", "localstack", "otel-collector"} {
		if !strings.Contains(line, want) {
			t.Fatalf("passo de integração sem %s: %s", want, line)
		}
	}
	for _, banned := range []string{"keycloak", "grafana"} {
		if strings.Contains(line, banned) {
			t.Fatalf("passo de integração inclui %s: %s", banned, line)
		}
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
	data, err := os.ReadFile(path) //nolint:gosec // caminho fixo do repositório de teste
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
