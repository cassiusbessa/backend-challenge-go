package main

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
			// getJSON returns only after the answer, and the handler reports the
			// header before it answers, so an empty channel is a request that was
			// never sent, not one still on its way.
			select {
			case got := <-heard:
				if got != tc.want {
					t.Errorf("Authorization = %q, want %q", got, tc.want)
				}
			default:
				t.Errorf("getJSON answered without a request reaching the server")
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

// reply is what one command of the environment answers.
type reply struct {
	stdout string
	stderr string
	err    error
}

// commandsAnswering stands in for docker, psql, awslocal and git. Each command
// line is matched against the keys, and the first key found in it answers; a
// command no key names fails the way a missing service does.
func commandsAnswering(replies map[string]reply) func([]string) (string, string, error) {
	return func(args []string) (string, string, error) {
		line := strings.Join(args, " ")
		for _, key := range slices.Sorted(maps.Keys(replies)) {
			if strings.Contains(line, key) {
				answered := replies[key]
				return answered.stdout, answered.stderr, answered.err
			}
		}
		return "", "service \"postgres\" is not running\nmore detail", errors.New("exit status 1")
	}
}

// healthyReplies is every command answered the way a stack in the state of the
// versioned files answers it.
func healthyReplies() map[string]reply {
	return map[string]reply{
		"datname='junglegaming'":      {stdout: "1\n"},
		"datname='junglegaming_test'": {stdout: "1\n"},
		"-d junglegaming -tAc":        {stdout: "6|false\n"},
		"-d junglegaming_test -tAc":   {stdout: "6|false\n"},
		"sqs list-queues":             {stdout: `{"QueueUrls":["http://localhost:4566/000000000000/wager-transactions.fifo"]}`},
		"sns list-topics":             {stdout: `{"Topics":[{"TopicArn":"arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"}]}`},
		"compose ps -q wager":         {stdout: "abc123\n"},
		"{{.Image}} abc123":           {stdout: "sha256:built\n"},
		"{{.Created}} sha256:built":   {stdout: "2026-09-26T15:00:00.123456789Z\n"},
		"log -1 --format=%cI":         {stdout: "2026-09-26T11:00:00-03:00\n"},
	}
}

// servicesAnswering is the identity provider, the Grafana and the Prometheus
// behind one server, each route answering the body a case hands it.
func servicesAnswering(t *testing.T, bodies map[string]string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func healthyBodies() map[string]string {
	return map[string]string{
		"/realms/master/protocol/openid-connect/token": `{"access_token":"a-token"}`,
		"/admin/realms/junglegaming":                   `{"accessTokenLifespan":300}`,
		"/api/search":                                  `[{"title":"Liquidação","type":"dash-db","uid":"liquidacao"}]`,
		"/api/v1/rules":                                `{"data":{"groups":[{"rules":[{"name":"ReconciliationDivergenceFound"}]}]}}`,
	}
}

// versionedRoot is a repository holding every versioned file the verifier
// reads, in the state the healthy replies and bodies match.
func versionedRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"deploy/migrations/000001_schema.up.sql":   "",
		"deploy/migrations/000001_schema.down.sql": "",
		"deploy/migrations/000006_last.up.sql":     "",
		"deploy/keycloak/junglegaming-realm.json":  `{"realm":"junglegaming","accessTokenLifespan":300}`,
		"deploy/terraform/localstack/sqs.tf":       "resource \"aws_sqs_queue\" \"ingress\" {\n  name = \"wager-transactions.fifo\"\n}\n",
		"deploy/terraform/localstack/sns.tf":       "resource \"aws_sns_topic\" \"events\" {\n  name = \"wallet-events.fifo\"\n}\n",
		"deploy/prometheus/rules/settlement.yml":   "groups:\n  - name: settlement\n    rules:\n      - alert: ReconciliationDivergenceFound\n",
		"Dockerfile":                               "FROM golang AS build\nCOPY go.mod ./\nCOPY internal ./internal\nFROM scratch\nCOPY --from=build /app /app\n",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("make the directory of %s: err = %v, want nil", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write the versioned %s: err = %v, want nil", name, err)
		}
	}
	return root
}

// stack is the options of one case: the versioned root, the services and the
// commands, from the defaults the command line gives.
func stack(t *testing.T, replies map[string]reply, bodies map[string]string) options {
	t.Helper()
	services := servicesAnswering(t, bodies)
	opts := readFlags([]string{"-root", versionedRoot(t), "-idp", services, "-grafana", services, "-prometheus", services, "-timeout", "5s"})
	opts.commands = commandsAnswering(replies)
	return opts
}

// with is the healthy replies with some of them changed by a case.
func with(changed map[string]reply) map[string]reply {
	replies := healthyReplies()
	maps.Copy(replies, changed)
	return replies
}

// bodiesWith is the healthy bodies with some of them changed by a case, and an
// empty body standing for a route the service does not have.
func bodiesWith(changed map[string]string) map[string]string {
	bodies := healthyBodies()
	for path, body := range changed {
		if body == "" {
			delete(bodies, path)
			continue
		}
		bodies[path] = body
	}
	return bodies
}

// A stack in the state of every versioned file answers no finding at all, which
// is the one answer make verify passes on.
func TestInspect_findsNothingInAnEnvironmentThatMatchesTheVersionedFiles(t *testing.T) {
	t.Parallel()
	if got := inspect(stack(t, healthyReplies(), healthyBodies())); len(got) != 0 {
		t.Errorf("findings of a matching environment = %v, want none", got)
	}
}

// A stack that is down is an answer too: every area that cannot be asked names
// itself, and a failed command is named by the first line it printed.
func TestInspect_namesEveryAreaThatCannotBeAsked(t *testing.T) {
	t.Parallel()
	got := inspect(stack(t, map[string]reply{}, map[string]string{}))
	for _, want := range []string{
		"datname='junglegaming'", "datname='junglegaming_test'",
		"administrator token", "sqs list-queues", "sns list-topics", "compose ps -q wager",
		"search the Grafana", "rules the Prometheus loaded",
	} {
		if !slices.ContainsFunc(got, func(finding string) bool { return strings.Contains(finding, want) }) {
			t.Errorf("findings of a stack that is down = %v, want one naming %q", got, want)
		}
	}
	if len(got) != 8 {
		t.Errorf("findings of a stack that is down = %d, want one per area that could not be asked, 8", len(got))
	}
	if joined := strings.Join(got, "\n"); strings.Contains(joined, "more detail") || !strings.Contains(joined, `service "postgres" is not running`) {
		t.Errorf("findings = %q, want each failed command named by its first line only", joined)
	}
}

// The findings go to the error stream and exit 1; no finding says so on the
// output stream and exits 0.
func TestReport_answersTheExitCodeAndWritesEachAnswerToItsStream(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := report([]string{"first", "second"}, &stdout, &stderr); code != 1 || stderr.String() != "first\nsecond\n" || stdout.Len() != 0 {
		t.Errorf("report of two findings = %d, stdout %q, stderr %q, want 1 with both on stderr", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := report(nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "matches") || stderr.Len() != 0 {
		t.Errorf("report of no finding = %d, stdout %q, stderr %q, want 0 with the match on stdout", code, stdout.String(), stderr.String())
	}
}

func TestReadFlags_readsTheDefaultsAndWhatTheCommandLineSets(t *testing.T) {
	t.Parallel()
	opts := readFlags([]string{"-root", "/repo", "-suite-db", "elsewhere", "-timeout", "3s"})
	if opts.root != "/repo" || opts.suite != "elsewhere" || opts.timeout != 3*time.Second {
		t.Errorf("options set on the command line = %+v, want the root, the suite database and the timeout set", opts)
	}
	defaults := readFlags(nil)
	if defaults.app != "junglegaming" || defaults.service != "wager" || defaults.rulesFile != "deploy/prometheus/rules/settlement.yml" || defaults.timeout != 30*time.Second {
		t.Errorf("options left to their defaults = %+v, want the defaults of the Compose and a deadline of 30s", defaults)
	}
}

// With no stand-in, run executes the process from the root, answers its output
// trimmed, and names a failure by the command, the exit and the first line of
// what it printed.
func TestRun_executesTheProcessFromTheRootAndNamesItsFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	o := options{root: root, timeout: 5 * time.Second}
	got, err := run(o, "sh", "-c", "pwd; echo noise >&2")
	if err != nil {
		t.Fatalf("run of a command that succeeds: err = %v, want nil", err)
	}
	if resolved, _ := filepath.EvalSymlinks(root); got != root && got != resolved {
		t.Errorf("working directory of the command = %q, want the root %q", got, root)
	}
	_, err = run(o, "sh", "-c", "echo first >&2; echo second >&2; exit 3")
	if err == nil || !strings.HasSuffix(err.Error(), "exit status 3: first") {
		t.Errorf("run of a command that fails = %v, want the exit and the first line it printed", err)
	}
}

// A database that is not there is a state to report, not a failure to ask; a
// database with no version table is the state a full reversal leaves.
func TestDatabaseState_answersEveryStateADatabaseCanBeIn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		replies map[string]reply
		want    schemaState
		fails   bool
	}{
		{name: "applied", replies: healthyReplies(), want: schemaState{name: "junglegaming", present: true, version: "6"}},
		{name: "not there", replies: with(map[string]reply{"datname='junglegaming'": {stdout: "\n"}}), want: schemaState{name: "junglegaming"}},
		{name: "no schema", replies: with(map[string]reply{"-d junglegaming -tAc": {stdout: noSchema}}), want: schemaState{name: "junglegaming", present: true, version: noSchema}},
		{name: "the version cannot be read", replies: with(map[string]reply{"-d junglegaming -tAc": {err: errors.New("exit status 2")}}), fails: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := options{user: "junglegaming", commands: commandsAnswering(tc.replies)}
			got, err := databaseState(o, "junglegaming")
			if (err != nil) != tc.fails {
				t.Fatalf("databaseState err = %v, want a failure %t", err, tc.fails)
			}
			if got != tc.want {
				t.Errorf("databaseState = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The versioned migrations are what a database is compared against, so a
// directory that declares none is a finding of its own.
func TestCheckDatabases_namesTheMigrationsThatDeclareNoVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{name: "no migration at all", files: map[string]string{"README": ""}, want: "has no migration"},
		{name: "a migration with no version", files: map[string]string{"schema.up.sql": ""}, want: "names a version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := stack(t, healthyReplies(), healthyBodies())
			o.root = writeProvisioning(t, tc.files)
			o.migrations = "."
			assertFinding(t, checkDatabases(o), tc.want)
		})
	}
}

// The realm is read from the file and from the identity provider, and each
// side that cannot be read, or that disagrees, is named.
func TestCheckRealm_namesTheLifespanThatCannotBeReadOrDisagrees(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		file   string
		bodies map[string]string
		want   string
	}{
		{name: "agrees", bodies: healthyBodies()},
		{name: "the versioned realm is not there", file: "absent.json", bodies: healthyBodies(), want: "read the versioned realm"},
		{name: "the realm issues another lifespan", bodies: bodiesWith(map[string]string{"/admin/realms/junglegaming": `{"accessTokenLifespan":60}`}), want: "issues tokens for 60s"},
		{name: "the realm reports no lifespan", bodies: bodiesWith(map[string]string{"/admin/realms/junglegaming": `{}`}), want: "reports no accessTokenLifespan"},
		{name: "the realm cannot be read", bodies: bodiesWith(map[string]string{"/admin/realms/junglegaming": ""}), want: "read the realm from the identity provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := stack(t, healthyReplies(), tc.bodies)
			if tc.file != "" {
				o.realmFile = tc.file
			}
			assertFinding(t, checkRealm(o), tc.want)
		})
	}
}

// Every queue and topic Terraform declares has to be in the broker; the side
// that cannot be read is named and the other side is still compared.
func TestCheckBroker_namesWhatTheBrokerDoesNotHaveOrDoesNotAnswer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		terraform string
		replies   map[string]reply
		want      []string
	}{
		{name: "both there", replies: healthyReplies()},
		{name: "no provisioning", terraform: "absent", replies: healthyReplies(), want: []string{"has no .tf"}},
		{name: "an empty broker", replies: with(map[string]reply{"sqs list-queues": {}, "sns list-topics": {}}), want: []string{"queue wager-transactions.fifo", "topic wallet-events.fifo"}},
		{name: "queues answered in something that is not JSON", replies: with(map[string]reply{"sqs list-queues": {stdout: "Traceback"}}), want: []string{"read what the broker answered to sqs list-queues"}},
		{name: "queues that cannot be asked", replies: with(map[string]reply{"sqs list-queues": {err: errors.New("exit status 255")}}), want: []string{"sqs list-queues"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := stack(t, tc.replies, healthyBodies())
			if tc.terraform != "" {
				o.terraform = tc.terraform
			}
			got := checkBroker(o)
			if len(got) != len(tc.want) {
				t.Fatalf("broker findings = %v, want %d", got, len(tc.want))
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("broker finding %d = %q, want it to carry %q", i, got[i], want)
				}
			}
		})
	}
}

// The running image is dated against the last commit of what it holds. A
// service that is not running, an answer that cannot be read and an image built
// before that commit are each named.
func TestCheckImage_namesTheImageThatCannotBeDatedOrIsStale(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		dockerfile string
		replies    map[string]reply
		want       string
	}{
		{name: "built after the commit", replies: healthyReplies()},
		{name: "not running", replies: with(map[string]reply{"compose ps -q wager": {stdout: "\n"}}), want: "is not running"},
		{name: "the container cannot be inspected", replies: with(map[string]reply{"{{.Image}} abc123": {err: errors.New("exit status 1")}}), want: "docker inspect"},
		{name: "the image cannot be inspected", replies: with(map[string]reply{"{{.Created}} sha256:built": {err: errors.New("exit status 1")}}), want: "docker image inspect"},
		{name: "a build time that is not a time", replies: with(map[string]reply{"{{.Created}} sha256:built": {stdout: "yesterday"}}), want: "read when the image of wager was built"},
		{name: "no recipe", dockerfile: "Absent", replies: healthyReplies(), want: "read the versioned image recipe"},
		{name: "no commit touches the recipe", replies: with(map[string]reply{"log -1 --format=%cI": {stdout: ""}}), want: "no commit touches"},
		{name: "a commit time that is not a time", replies: with(map[string]reply{"log -1 --format=%cI": {stdout: "today"}}), want: "read when the commit was made"},
		{name: "the history cannot be read", replies: with(map[string]reply{"log -1 --format=%cI": {err: errors.New("exit status 128")}}), want: "git -C"},
		{name: "built before the commit", replies: with(map[string]reply{"log -1 --format=%cI": {stdout: "2026-09-26T16:00:00Z"}}), want: "before the commit at 2026-09-26T16:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := stack(t, tc.replies, healthyBodies())
			if tc.dockerfile != "" {
				o.dockerfile = tc.dockerfile
			}
			assertFinding(t, checkImage(o), tc.want)
		})
	}
}

// execute answers what the process printed as it printed it, trimming nothing,
// and hands the command to the stand-in of a case instead of running it.
func TestExecute_answersThePrintedStreamsOfTheStandInOrOfTheProcess(t *testing.T) {
	t.Parallel()
	var asked []string
	standIn := options{commands: func(args []string) (string, string, error) {
		asked = args
		return " out \n", " err \n", nil
	}}
	stdout, stderr, err := standIn.execute([]string{"docker", "compose", "ps"})
	if err != nil || stdout != " out \n" || stderr != " err \n" || strings.Join(asked, " ") != "docker compose ps" {
		t.Errorf("execute with a stand-in = %q, %q, %v after asking %v, want both streams untrimmed and the command handed over", stdout, stderr, err, asked)
	}
	process := options{root: t.TempDir(), timeout: 5 * time.Second}
	stdout, stderr, err = process.execute([]string{"sh", "-c", "echo out; echo err >&2"})
	if err != nil || stdout != "out\n" || stderr != "err\n" {
		t.Errorf("execute of the process = %q, %q, %v, want each stream as printed", stdout, stderr, err)
	}
}
