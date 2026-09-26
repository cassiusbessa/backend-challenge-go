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

// writeRealm puts a realm in a file of its own and answers the path, so a case
// hands the reader a file that only it owns.
func writeRealm(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "realm.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the realm the case reads: err = %v, want nil", err)
	}
	return path
}

// writeProvisioning fills a directory with the files a case wants read, and
// answers the directory.
func writeProvisioning(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write the provisioning the case reads: err = %v, want nil", err)
		}
	}
	return dir
}

func TestDeclaredLifespan_readsTheLifespanTheRealmDeclares(t *testing.T) {
	t.Parallel()
	got, err := declaredLifespan(writeRealm(t, `{"realm":"junglegaming","accessTokenLifespan":300}`))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != 300 {
		t.Errorf("declared lifespan = %d, want 300", got)
	}
}

func TestDeclaredLifespan_readsZeroAsAValueAndNotAnAbsence(t *testing.T) {
	t.Parallel()
	got, err := declaredLifespan(writeRealm(t, `{"accessTokenLifespan":0}`))
	if err != nil {
		t.Fatalf("err on a lifespan of zero = %v, want nil", err)
	}
	if got != 0 {
		t.Errorf("declared lifespan of zero = %d, want 0", got)
	}
}

func TestDeclaredLifespan_refusesARealmThatDeclaresNone(t *testing.T) {
	t.Parallel()
	if _, err := declaredLifespan(writeRealm(t, `{"realm":"junglegaming"}`)); err == nil {
		t.Errorf("err on a realm without the field = %v, want one", err)
	}
}

func TestDeclaredLifespan_refusesAFileThatIsNotJSON(t *testing.T) {
	t.Parallel()
	if _, err := declaredLifespan(writeRealm(t, "this is not a realm")); err == nil {
		t.Errorf("err on a file that is not JSON = %v, want one", err)
	}
}

func TestDeclaredLifespan_refusesAPathThatIsNotThere(t *testing.T) {
	t.Parallel()
	if _, err := declaredLifespan(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Errorf("err on a path that does not exist = %v, want one", err)
	}
}

func TestTerraformSource_readsEveryVersionedFileAsOneText(t *testing.T) {
	t.Parallel()
	dir := writeProvisioning(t, map[string]string{
		"queues.tf":   `resource "aws_sqs_queue" "transactions" {}`,
		"topics.tf":   `resource "aws_sns_topic" "events" {}`,
		"versions.tf": `terraform { required_version = ">= 1.13" }`,
		"README.md":   "not provisioning",
	})
	got, err := terraformSource(dir)
	if err != nil {
		t.Fatalf("err on a directory of provisioning = %v, want nil", err)
	}
	if !strings.Contains(got, "aws_sqs_queue") {
		t.Errorf("source = %q, want the queue resource in it", got)
	}
	if !strings.Contains(got, "aws_sns_topic") {
		t.Errorf("source = %q, want the topic resource in it", got)
	}
	if strings.Contains(got, "not provisioning") {
		t.Errorf("source = %q, want the file that is not .tf left out", got)
	}
}

func TestTerraformSource_refusesADirectoryThatDeclaresNothing(t *testing.T) {
	t.Parallel()
	if _, err := terraformSource(t.TempDir()); err == nil {
		t.Errorf("err on a directory without .tf = %v, want one", err)
	}
}

// idpAnswering stands in for the identity provider with one canned body, which is
// the only part of the grant these cases are about.
func idpAnswering(t *testing.T, body string) string {
	t.Helper()
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(idp.Close)
	return idp.URL
}

func TestAdminToken_refusesAGrantAnsweredWithoutAToken(t *testing.T) {
	t.Parallel()
	idp := idpAnswering(t, `{"token_type":"Bearer"}`)
	if _, err := adminToken(options{idp: idp, timeout: 5 * time.Second}); err == nil {
		t.Errorf("err on a grant answered without a token = %v, want one", err)
	}
}

func TestAdminToken_answersTheTokenTheGrantCarries(t *testing.T) {
	t.Parallel()
	idp := idpAnswering(t, `{"access_token":"a-token"}`)
	got, err := adminToken(options{idp: idp, timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("err on a grant answered with a token = %v, want nil", err)
	}
	if got != "a-token" {
		t.Errorf("token = %q, want a-token", got)
	}
}
