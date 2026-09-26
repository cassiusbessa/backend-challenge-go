// Command envcheck answers whether the local environment is in the state the
// versioned files declare, and changes nothing.
//
// It reaches the services through the same commands the README publishes, and it
// depends on the standard library alone, so it keeps answering when the
// application does not compile or does not come up — which is when it is called.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type options struct {
	root        string
	user        string
	app         string
	suite       string
	idp         string
	realm       string
	realmFile   string
	migrations  string
	adminUser   string
	adminPass   string
	terraform   string
	service     string
	dockerfile  string
	grafana     string
	grafanaUser string
	grafanaPass string
	dashboard   string
	prometheus  string
	rulesFile   string
	timeout     time.Duration
}

func main() {
	opts := readFlags()
	findings := inspect(opts)
	for _, each := range findings {
		fmt.Fprintln(os.Stderr, each)
	}
	if len(findings) > 0 {
		os.Exit(1)
	}
	fmt.Println("environment matches the versioned files")
}

func readFlags() options {
	var opts options
	flag.StringVar(&opts.root, "root", ".", "repository root")
	flag.StringVar(&opts.user, "postgres-user", "junglegaming", "role that connects to PostgreSQL")
	flag.StringVar(&opts.app, "app-db", "junglegaming", "database the application uses")
	flag.StringVar(&opts.suite, "suite-db", "junglegaming_test", "database the journey suite uses")
	flag.StringVar(&opts.idp, "idp", "http://localhost:8080", "base address of the identity provider")
	flag.StringVar(&opts.realm, "realm", "junglegaming", "realm that issues the tokens")
	flag.StringVar(&opts.realmFile, "realm-file", "deploy/keycloak/junglegaming-realm.json", "versioned realm, relative to root")
	flag.StringVar(&opts.adminUser, "admin-user", "admin", "bootstrap administrator of the identity provider")
	flag.StringVar(&opts.adminPass, "admin-password", "admin", "password of that administrator")
	flag.StringVar(&opts.migrations, "migrations", "deploy/migrations", "versioned migrations, relative to root")
	flag.StringVar(&opts.terraform, "terraform", "deploy/terraform/localstack", "versioned provisioning, relative to root")
	flag.StringVar(&opts.service, "service", "wager", "Compose service that runs the application")
	flag.StringVar(&opts.dockerfile, "dockerfile", "Dockerfile", "versioned image recipe, relative to root")
	flag.StringVar(&opts.grafana, "grafana", "http://localhost:3000", "base address of the Grafana")
	flag.StringVar(&opts.grafanaUser, "grafana-user", "admin", "administrator of the Grafana")
	flag.StringVar(&opts.grafanaPass, "grafana-password", "admin", "password of that administrator")
	flag.StringVar(&opts.dashboard, "dashboard", "Liquidação", "title of the provisioned dashboard")
	flag.StringVar(&opts.prometheus, "prometheus", "http://localhost:9095", "base address of the Prometheus")
	flag.StringVar(&opts.rulesFile, "rules-file", "deploy/prometheus/rules/settlement.yml", "versioned alert rules, relative to root")
	flag.DurationVar(&opts.timeout, "timeout", 30*time.Second, "deadline of each command")
	flag.Parse()
	return opts
}

// inspect runs every area and collects what diverged. An area that cannot be
// asked at all reports that as a finding of its own: the environment being
// unreachable is one of the answers this command exists to give.
func inspect(o options) []string {
	var out []string
	out = append(out, checkDatabases(o)...)
	out = append(out, checkRealm(o)...)
	out = append(out, checkBroker(o)...)
	out = append(out, checkImage(o)...)
	out = append(out, checkDashboard(o)...)
	out = append(out, checkRules(o)...)
	return out
}

// checkDashboard asks the Grafana whether the versioned dashboard is there as a
// provisioned one. The search is by title, and the answer is compared by title
// and by provenance: a dashboard of the same name saved by hand is not the file.
func checkDashboard(o options) []string {
	var found []dashboardHit
	endpoint := o.grafana + "/api/search?query=" + url.QueryEscape(o.dashboard)
	if err := getBasicJSON(o, endpoint, &found); err != nil {
		return []string{fmt.Errorf("search the Grafana for the dashboard: %w", err).Error()}
	}
	return compareDashboard(o.dashboard, found)
}

// dashboardHit is one row of the search of the Grafana, as much of it as the
// comparison reads.
type dashboardHit struct {
	Title string `json:"title"`
	Type  string `json:"type"`
	UID   string `json:"uid"`
}

// checkRules reads the alert names the versioned file declares and asks the
// Prometheus which rules it loaded. A file that failed to load leaves the
// Prometheus with the previous set, or with none, and neither says so on its
// own.
func checkRules(o options) []string {
	source, err := os.ReadFile(filepath.Join(o.root, o.rulesFile)) //nolint:gosec // the path comes from a flag the operator controls
	if err != nil {
		return []string{fmt.Errorf("read the versioned alert rules: %w", err).Error()}
	}
	declared := declaredAlerts(string(source))
	if len(declared) == 0 {
		return []string{fmt.Sprintf("%s declares no alert, so nothing says what the Prometheus should load", o.rulesFile)}
	}
	loaded, err := loadedRules(o)
	if err != nil {
		return []string{err.Error()}
	}
	return missingRules(declared, loaded)
}

// loadedRules answers the name of every rule the Prometheus has loaded, across
// every group.
func loadedRules(o options) ([]string, error) {
	var body struct {
		Data struct {
			Groups []struct {
				Rules []struct {
					Name string `json:"name"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := getJSON(o, o.prometheus+"/api/v1/rules", "", &body); err != nil {
		return nil, fmt.Errorf("read the rules the Prometheus loaded: %w", err)
	}
	var names []string
	for _, group := range body.Data.Groups {
		for _, rule := range group.Rules {
			names = append(names, rule.Name)
		}
	}
	return names, nil
}

// getBasicJSON is getJSON with the basic credential the Grafana takes, which is
// the example password of the Compose and not a bearer.
func getBasicJSON(o options, endpoint string, into any) error {
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(o.grafanaUser, o.grafanaPass)
	return send(req, into)
}

func checkDatabases(o options) []string {
	var out []string
	states := make([]schemaState, 0, 2)
	for _, name := range []string{o.app, o.suite} {
		state, err := databaseState(o, name)
		if err != nil {
			out = append(out, err.Error())
			continue
		}
		states = append(states, state)
	}
	out = append(out, compareSchema(states)...)
	names, err := migrationNames(o)
	if err != nil {
		return append(out, err.Error())
	}
	declared, err := declaredMigration(names)
	if err != nil {
		return append(out, err.Error())
	}
	return append(out, compareDeclared(declared, states)...)
}

// migrationNames lists the versioned migrations that move the schema forward. The
// `down` files are left out: they carry the same versions and would say nothing
// more about how current a database is.
func migrationNames(o options) ([]string, error) {
	names, err := filepath.Glob(filepath.Join(o.root, o.migrations, "*.up.sql"))
	if err != nil {
		return nil, fmt.Errorf("read the versioned migrations: %w", err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s has no migration, so nothing declares the schema", filepath.Join(o.root, o.migrations))
	}
	return names, nil
}

func databaseState(o options, name string) (schemaState, error) {
	exists, err := databaseExists(o, name)
	if err != nil {
		return schemaState{}, err
	}
	if !exists {
		return schemaState{name: name}, nil
	}
	// One query rather than two, so a version table that is not there answers
	// instead of failing: a database without schema is a state, not an error.
	raw, err := psql(o, name, "SELECT CASE WHEN to_regclass('public.schema_migrations') IS NULL THEN '"+noSchema+"'"+
		" ELSE (SELECT version || '|' || dirty FROM schema_migrations) END")
	if err != nil {
		return schemaState{}, err
	}
	return parseSchemaRow(name, raw)
}

func databaseExists(o options, name string) (bool, error) {
	// The question is asked of `postgres`, which is always there, so a missing
	// database answers false instead of failing to connect.
	raw, err := psql(o, "postgres", "SELECT 1 FROM pg_database WHERE datname='"+name+"'")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(raw) == "1", nil
}

func psql(o options, database, query string) (string, error) {
	return run(o, "docker", "compose", "exec", "-T", "postgres",
		"psql", "-U", o.user, "-d", database, "-tAc", query)
}

func checkRealm(o options) []string {
	declared, err := declaredLifespan(filepath.Join(o.root, o.realmFile))
	if err != nil {
		return []string{err.Error()}
	}
	observed, err := observedLifespan(o)
	if err != nil {
		return []string{err.Error()}
	}
	return compareLifespan(o.realm, declared, observed)
}

func declaredLifespan(path string) (int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path comes from a flag the operator controls
	if err != nil {
		return 0, fmt.Errorf("read the versioned realm: %w", err)
	}
	var body struct {
		AccessTokenLifespan *int `json:"accessTokenLifespan"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return 0, fmt.Errorf("read the versioned realm: %w", err)
	}
	if body.AccessTokenLifespan == nil {
		return 0, fmt.Errorf("%s declares no accessTokenLifespan, so nothing says what the realm should issue", path)
	}
	return *body.AccessTokenLifespan, nil
}

func observedLifespan(o options) (int, error) {
	token, err := adminToken(o)
	if err != nil {
		return 0, err
	}
	var body struct {
		AccessTokenLifespan *int `json:"accessTokenLifespan"`
	}
	if err := getJSON(o, o.idp+"/admin/realms/"+o.realm, token, &body); err != nil {
		return 0, fmt.Errorf("read the realm from the identity provider: %w", err)
	}
	if body.AccessTokenLifespan == nil {
		return 0, fmt.Errorf("realm %s reports no accessTokenLifespan", o.realm)
	}
	return *body.AccessTokenLifespan, nil
}

// adminToken asks the bootstrap administrator of the master realm for a token,
// which is the same grant the journey suite uses to reach the admin API.
func adminToken(o options) (string, error) {
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {o.adminUser},
		"password":   {o.adminPass},
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	endpoint := o.idp + "/realms/master/protocol/openid-connect/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("ask the identity provider for an administrator token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := send(req, &body); err != nil {
		return "", fmt.Errorf("ask the identity provider for an administrator token: %w", err)
	}
	// A wrong password is answered with a non-200 and is already a finding. This
	// is the 200 whose body carries no token, which a proxy in front of the realm
	// produces: without the guard the caller sends an empty bearer and the 401
	// that comes back names the realm for a credential problem.
	if body.AccessToken == "" {
		return "", fmt.Errorf("the administrator grant was answered with no access_token")
	}
	return body.AccessToken, nil
}

func getJSON(o options, endpoint, token string, into any) error {
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	// The Prometheus takes no credential, and an empty bearer would be a header
	// carrying nothing.
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return send(req, into)
}

func send(req *http.Request, into any) error {
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(into)
}

func checkBroker(o options) []string {
	sources, err := terraformSource(filepath.Join(o.root, o.terraform))
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	queues, err := brokerQueues(o)
	if err != nil {
		out = append(out, err.Error())
	} else {
		out = append(out, missingBroker("queue", declaredNames("aws_sqs_queue", sources), queues)...)
	}
	topics, err := brokerTopics(o)
	if err != nil {
		return append(out, err.Error())
	}
	return append(out, missingBroker("topic", declaredNames("aws_sns_topic", sources), topics)...)
}

// terraformSource reads every versioned `.tf` of the provisioning as one text, so
// the declared names come from the files that create them.
func terraformSource(dir string) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return "", fmt.Errorf("read the versioned provisioning: %w", err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("%s has no .tf, so nothing declares the queues and the topic", dir)
	}
	var all strings.Builder
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // the path comes from a flag the operator controls
		if err != nil {
			return "", fmt.Errorf("read the versioned provisioning: %w", err)
		}
		all.Write(data)
		all.WriteString("\n")
	}
	return all.String(), nil
}

func brokerQueues(o options) ([]string, error) {
	var body struct {
		QueueUrls []string `json:"QueueUrls"`
	}
	if err := awslocal(o, &body, "sqs", "list-queues"); err != nil {
		return nil, err
	}
	return body.QueueUrls, nil
}

func brokerTopics(o options) ([]string, error) {
	var body struct {
		Topics []struct {
			TopicArn string `json:"TopicArn"`
		} `json:"Topics"`
	}
	if err := awslocal(o, &body, "sns", "list-topics"); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Topics))
	for _, each := range body.Topics {
		out = append(out, each.TopicArn)
	}
	return out, nil
}

// awslocal asks the broker through the client its own image ships, so the
// verifier needs no AWS SDK and no AWS command line on the host.
func awslocal(o options, into any, args ...string) error {
	raw, err := run(o, append([]string{"docker", "compose", "exec", "-T", "localstack", "awslocal"}, args...)...)
	if err != nil {
		return err
	}
	// An empty broker answers nothing at all rather than an empty object.
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		return fmt.Errorf("read what the broker answered to %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func checkImage(o options) []string {
	built, err := imageBuiltAt(o)
	if err != nil {
		return []string{err.Error()}
	}
	if built.IsZero() {
		return []string{fmt.Sprintf("service %s is not running, so no image answers for the commit", o.service)}
	}
	sources, err := imagePaths(o)
	if err != nil {
		return []string{err.Error()}
	}
	commit, err := commitTime(o, sources)
	if err != nil {
		return []string{err.Error()}
	}
	return staleImage(o.service, built, commit)
}

// imageBuiltAt answers the zero time when the service is not running, which is a
// state to report rather than a failure to read.
func imageBuiltAt(o options) (time.Time, error) {
	container, err := run(o, "docker", "compose", "ps", "-q", o.service)
	if err != nil {
		return time.Time{}, err
	}
	first := firstLine(container)
	if first == "" {
		return time.Time{}, nil
	}
	image, err := run(o, "docker", "inspect", "-f", "{{.Image}}", first)
	if err != nil {
		return time.Time{}, err
	}
	created, err := run(o, "docker", "image", "inspect", "-f", "{{.Created}}", image)
	if err != nil {
		return time.Time{}, err
	}
	built, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return time.Time{}, fmt.Errorf("read when the image of %s was built: %w", o.service, err)
	}
	return built, nil
}

// imagePaths is the Dockerfile plus everything it copies in: the set whose last
// commit the running image has to be at least as new as.
func imagePaths(o options) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(o.root, o.dockerfile))
	if err != nil {
		return nil, fmt.Errorf("read the versioned image recipe: %w", err)
	}
	return append([]string{o.dockerfile}, imageSources(string(data))...), nil
}

func commitTime(o options, paths []string) (time.Time, error) {
	args := append([]string{"git", "-C", o.root, "log", "-1", "--format=%cI", "--"}, paths...)
	raw, err := run(o, args...)
	if err != nil {
		return time.Time{}, err
	}
	if raw == "" {
		return time.Time{}, fmt.Errorf("no commit touches %s, so nothing dates the image", strings.Join(paths, " "))
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("read when the commit was made: %w", err)
	}
	return at, nil
}

// run executes one command from the repository root and returns its trimmed
// output. Every question the verifier asks of the environment goes through here,
// so a service that is not up produces a finding instead of a panic.
func run(o options, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	// Every command here is a literal of this file; what varies is a database name
	// or a path the operator passed on a flag. Asking the environment is the job.
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // the operator names what is asked
	cmd.Dir = o.root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, firstLine(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
