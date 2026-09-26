package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTemp puts content in a file of its own and answers the path, so a case can
// hand a reader a file that only it owns.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the file the case reads: err = %v, want nil", err)
	}
	return path
}

func TestDeclaredLifespan_readsWhatTheVersionedRealmDeclares(t *testing.T) {
	t.Parallel()
	t.Run("a realm that declares it", func(t *testing.T) {
		got, err := declaredLifespan(writeTemp(t, "realm.json", `{"realm":"junglegaming","accessTokenLifespan":300}`))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != 300 {
			t.Errorf("declared lifespan = %d, want 300", got)
		}
	})

	t.Run("a lifespan of zero is a value and not an absence", func(t *testing.T) {
		got, err := declaredLifespan(writeTemp(t, "realm.json", `{"accessTokenLifespan":0}`))
		if err != nil {
			t.Fatalf("err on a lifespan of zero = %v, want nil", err)
		}
		if got != 0 {
			t.Errorf("declared lifespan of zero = %d, want 0", got)
		}
	})

	t.Run("a realm that declares none", func(t *testing.T) {
		if _, err := declaredLifespan(writeTemp(t, "realm.json", `{"realm":"junglegaming"}`)); err == nil {
			t.Errorf("err on a realm without the field = %v, want one", err)
		}
	})

	t.Run("a realm that is not JSON", func(t *testing.T) {
		if _, err := declaredLifespan(writeTemp(t, "realm.json", "this is not a realm")); err == nil {
			t.Errorf("err on a file that is not JSON = %v, want one", err)
		}
	})

	t.Run("a file that is not there", func(t *testing.T) {
		if _, err := declaredLifespan(filepath.Join(t.TempDir(), "absent.json")); err == nil {
			t.Errorf("err on a path that does not exist = %v, want one", err)
		}
	})
}

func TestTerraformSource_readsEveryVersionedFileAsOneText(t *testing.T) {
	t.Parallel()
	t.Run("the files become one text", func(t *testing.T) {
		dir := t.TempDir()
		for name, content := range map[string]string{
			"queues.tf":   `resource "aws_sqs_queue" "transactions" {}`,
			"topics.tf":   `resource "aws_sns_topic" "events" {}`,
			"README.md":   "not provisioning",
			"versions.tf": `terraform { required_version = ">= 1.13" }`,
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatalf("write the provisioning the case reads: err = %v, want nil", err)
			}
		}
		got, err := terraformSource(dir)
		if err != nil {
			t.Fatalf("err on a directory of provisioning = %v, want nil", err)
		}
		if !strings.Contains(got, "aws_sqs_queue") || !strings.Contains(got, "aws_sns_topic") {
			t.Errorf("source = %q, want both resources in it", got)
		}
		if strings.Contains(got, "not provisioning") {
			t.Errorf("source = %q, want the file that is not .tf left out", got)
		}
	})

	t.Run("a directory that declares nothing", func(t *testing.T) {
		if _, err := terraformSource(t.TempDir()); err == nil {
			t.Errorf("err on a directory without .tf = %v, want one", err)
		}
	})
}

func TestAdminToken_refusesAGrantAnsweredWithoutAToken(t *testing.T) {
	t.Parallel()
	t.Run("a 200 whose body carries no token", func(t *testing.T) {
		idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"token_type":"Bearer"}`)
		}))
		defer idp.Close()
		if _, err := adminToken(options{idp: idp.URL, timeout: 5 * time.Second}); err == nil {
			t.Errorf("err on a grant answered without a token = %v, want one", err)
		}
	})

	t.Run("a 200 whose body carries one", func(t *testing.T) {
		idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"access_token":"a-token"}`)
		}))
		defer idp.Close()
		got, err := adminToken(options{idp: idp.URL, timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("err on a grant answered with a token = %v, want nil", err)
		}
		if got != "a-token" {
			t.Errorf("token = %q, want a-token", got)
		}
	})
}
