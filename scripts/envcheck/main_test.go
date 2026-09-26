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

// The Prometheus answers its rules grouped, and the names are what the
// comparison reads: every group, every rule, in order.
func TestLoadedRules_readsEveryRuleOfEveryGroup(t *testing.T) {
	t.Parallel()
	prometheus := idpAnswering(t, `{"status":"success","data":{"groups":[
		{"name":"settlement","rules":[{"name":"ReconciliationDivergenceFound","type":"alerting"},{"name":"OutboxOldestPendingTooOld","type":"alerting"}]},
		{"name":"other","rules":[{"name":"job:up","type":"recording"}]}]}}`)
	got, err := loadedRules(options{prometheus: prometheus, timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("loadedRules err = %v, want nil", err)
	}
	if strings.Join(got, ",") != "ReconciliationDivergenceFound,OutboxOldestPendingTooOld,job:up" {
		t.Errorf("rules = %v, want the three in the order of the answer", got)
	}
}

func TestLoadedRules_answersTheFailureOfAPrometheusThatIsNotThere(t *testing.T) {
	t.Parallel()
	if _, err := loadedRules(options{prometheus: "http://127.0.0.1:1", timeout: time.Second}); err == nil {
		t.Errorf("err of a Prometheus nothing answers on = %v, want one", err)
	}
}

// getJSON sends the bearer only when there is one: the realm asks for it, and the
// Prometheus takes no credential, where an empty bearer would be a header
// carrying nothing.
func TestGetJSON_sendsTheBearerOnlyWhenThereIsAToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{name: "no token sends no header", token: "", want: ""},
		{name: "a token is sent as the bearer", token: "a-token", want: "Bearer a-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			heard := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				heard <- r.Header.Get("Authorization")
				fmt.Fprint(w, `{}`)
			}))
			t.Cleanup(server.Close)
			var body struct{}
			if err := getJSON(options{timeout: 5 * time.Second}, server.URL, tc.token, &body); err != nil {
				t.Fatalf("getJSON err = %v, want nil", err)
			}
			if got := <-heard; got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

// The search of the Grafana is asked with the basic credential of the example
// password, and the hits come back as the comparison reads them.
func TestCheckDashboard_readsTheHitsOfTheSearch(t *testing.T) {
	t.Parallel()
	grafana := idpAnswering(t, `[{"title":"Liquidação","type":"dash-db","uid":"liquidacao"}]`)
	got := checkDashboard(options{grafana: grafana, grafanaUser: "admin", grafanaPass: "admin", dashboard: "Liquidação", timeout: 5 * time.Second})
	if len(got) != 0 {
		t.Errorf("findings with the dashboard answered = %v, want none", got)
	}
	got = checkDashboard(options{grafana: "http://127.0.0.1:1", dashboard: "Liquidação", timeout: time.Second})
	if len(got) != 1 || !strings.Contains(got[0], "search the Grafana") {
		t.Errorf("findings with no Grafana = %v, want one naming the search", got)
	}
}

// checkRules reads the names off the versioned file and asks the Prometheus
// which it loaded: the file it cannot read, a file that declares no alert and
// an alert the Prometheus did not load are each a finding, named.
func TestCheckRules_comparesTheVersionedFileWithWhatThePrometheusLoaded(t *testing.T) {
	t.Parallel()
	declared := "groups:\n  - name: settlement\n    rules:\n      - alert: ReconciliationDivergenceFound\n      - alert: OutboxOldestPendingTooOld\n"
	loadedOne := idpAnswering(t, `{"data":{"groups":[{"rules":[{"name":"ReconciliationDivergenceFound"}]}]}}`)
	loadedBoth := idpAnswering(t, `{"data":{"groups":[{"rules":[{"name":"ReconciliationDivergenceFound"},{"name":"OutboxOldestPendingTooOld"}]}]}}`)
	root := writeProvisioning(t, map[string]string{"settlement.yml": declared, "empty.yml": "groups: []\n"})
	cases := []struct {
		name       string
		file       string
		prometheus string
		want       string
	}{
		{name: "both loaded", file: "settlement.yml", prometheus: loadedBoth},
		{name: "one left behind", file: "settlement.yml", prometheus: loadedOne, want: "OutboxOldestPendingTooOld"},
		{name: "a file that is not there", file: "absent.yml", prometheus: loadedBoth, want: "read the versioned alert rules"},
		{name: "a file that declares no alert", file: "empty.yml", prometheus: loadedBoth, want: "declares no alert"},
		{name: "a Prometheus that does not answer", file: "settlement.yml", prometheus: "http://127.0.0.1:1", want: "rules the Prometheus loaded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkRules(options{root: root, rulesFile: tc.file, prometheus: tc.prometheus, timeout: time.Second})
			assertFinding(t, got, tc.want)
		})
	}
}

// assertFinding demands no finding when none is wanted, and exactly one that
// carries the words wanted otherwise.
func assertFinding(t *testing.T, got []string, want string) {
	t.Helper()
	if want == "" {
		if len(got) != 0 {
			t.Errorf("findings = %v, want none", got)
		}
		return
	}
	if len(got) != 1 || !strings.Contains(got[0], want) {
		t.Errorf("findings = %v, want exactly one carrying %q", got, want)
	}
}
