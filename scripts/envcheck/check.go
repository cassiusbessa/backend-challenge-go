package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// schemaState is what one database answers about the schema applied to it. The
// zero value means a database that is not there: present is false and the
// version is unknown.
type schemaState struct {
	name    string
	present bool
	version string
	dirty   bool
}

// noSchema is the version of a database that exists and carries no migration,
// which is the state a full reversal leaves behind.
const noSchema = "none"

// parseSchemaRow reads what psql prints for the schema version in its unaligned
// tuples-only form, which is "6|false" for version six applied cleanly. An empty
// answer is a database whose version table has no row.
func parseSchemaRow(name, raw string) (schemaState, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == noSchema {
		return schemaState{name: name, present: true, version: noSchema}, nil
	}
	fields := strings.Split(trimmed, "|")
	if len(fields) != 2 || fields[0] == "" {
		return schemaState{}, fmt.Errorf("database %s: schema version row is %q", name, trimmed)
	}
	// Concatenating the flag casts it to text, which renders `true` and `false`,
	// not the `t` and `f` the output function of psql prints for a bare column.
	// ParseBool takes either, so the reading holds whichever way it is asked.
	dirty, err := strconv.ParseBool(fields[1])
	if err != nil {
		return schemaState{}, fmt.Errorf("database %s: schema version row carries %q for the dirty flag", name, fields[1])
	}
	return schemaState{name: name, present: true, version: fields[0], dirty: dirty}, nil
}

// compareSchema names every database that is missing, left dirty, or on a
// version the others are not.
func compareSchema(states []schemaState) []string {
	var out []string
	for _, each := range states {
		if !each.present {
			out = append(out, fmt.Sprintf("database %s does not exist", each.name))
			continue
		}
		if each.version == noSchema {
			out = append(out, fmt.Sprintf("database %s exists with no migration applied", each.name))
			continue
		}
		if each.dirty {
			out = append(out, fmt.Sprintf("database %s is at schema version %s and marked dirty", each.name, each.version))
		}
	}
	return append(out, disagreeingVersions(states)...)
}

// migrationFile matches a versioned migration and captures its version without
// the leading zeros, which is the form golang-migrate records in the table.
var migrationFile = regexp.MustCompile(`^0*(\d+)_.*\.up\.sql$`)

// declaredMigration reads the highest version the versioned migrations declare,
// which is the version a database that is up to date reports.
func declaredMigration(names []string) (string, error) {
	highest := -1
	for _, each := range names {
		found := migrationFile.FindStringSubmatch(filepath.Base(each))
		if found == nil {
			continue
		}
		// The capture is digits by construction, so this only answers for a version
		// too long to fit an int. It stays because dropping it would discard the
		// error, not because a migration is expected to reach it.
		version, err := strconv.Atoi(found[1])
		if err != nil {
			return "", fmt.Errorf("read the version of migration %s: %w", filepath.Base(each), err)
		}
		if version > highest {
			highest = version
		}
	}
	if highest < 0 {
		return "", fmt.Errorf("no versioned migration names a version, so nothing says how current a database is")
	}
	return strconv.Itoa(highest), nil
}

// compareDeclared names every database that is not at the version the migrations
// declare. Comparing the databases only against each other cannot see a migration
// applied to neither of them, which breaks exactly as much as one applied to one.
// A database that is missing or carries no migration is already named elsewhere.
func compareDeclared(declared string, states []schemaState) []string {
	var out []string
	for _, each := range states {
		if !each.present || each.version == noSchema || each.version == declared {
			continue
		}
		out = append(out, fmt.Sprintf("database %s is at schema version %s, and the versioned migrations declare %s",
			each.name, each.version, declared))
	}
	return out
}

// disagreeingVersions names the databases that are not on the same schema
// version. A migration applied to one and not the other fails much later, in a
// `relation does not exist` that never mentions a database.
func disagreeingVersions(states []schemaState) []string {
	applied := appliedOnly(states)
	if len(applied) < 2 || sameVersion(applied) {
		return nil
	}
	parts := make([]string, 0, len(applied))
	for _, each := range applied {
		parts = append(parts, each.name+" at "+each.version)
	}
	return []string{"the databases are on different schema versions: " + strings.Join(parts, ", ")}
}

func appliedOnly(states []schemaState) []schemaState {
	out := make([]schemaState, 0, len(states))
	for _, each := range states {
		if each.present && each.version != noSchema {
			out = append(out, each)
		}
	}
	return out
}

func sameVersion(states []schemaState) bool {
	for _, each := range states[1:] {
		if each.version != states[0].version {
			return false
		}
	}
	return true
}

// compareLifespan names a realm whose token lifespan is not the one its
// versioned file declares.
//
// An interrupted journey suite is the usual cause: the wallet suite shortens the
// lifespan to prove the border refuses an expired token and restores it at the
// end, so a run that died in the middle leaves every later token expiring at
// once, and every suite that asks for one gets a 401 that says nothing about the
// realm.
func compareLifespan(realm string, declared, observed int) []string {
	if declared == observed {
		return nil
	}
	return []string{fmt.Sprintf(
		"realm %s issues tokens for %ds, and its versioned file declares %ds",
		realm, observed, declared)}
}

// missingBroker names the queues or topics the versioned Terraform declares and
// the broker does not have.
func missingBroker(kind string, want, have []string) []string {
	var out []string
	for _, each := range want {
		if !named(have, each) {
			out = append(out, fmt.Sprintf("%s %s is declared in Terraform and the broker does not have it", kind, each))
		}
	}
	return out
}

// named answers whether the broker reported the declared name. A queue comes
// back as a URL and a topic as an ARN, and both end in the name Terraform gives.
func named(have []string, name string) bool {
	for _, each := range have {
		if each == name || strings.HasSuffix(each, "/"+name) || strings.HasSuffix(each, ":"+name) {
			return true
		}
	}
	return false
}

// staleImage names an image built before the commit the working tree is on.
//
// The image carries no commit of its own, so the build time against the commit
// time is the comparison available: one built before HEAD cannot contain it.
func staleImage(service string, built, commit time.Time) []string {
	if !built.Before(commit) {
		return nil
	}
	return []string{fmt.Sprintf(
		"the image %s runs was built at %s, before the commit at %s",
		service, stamp(built), stamp(commit))}
}

func stamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339)
}

var (
	resourceHead = regexp.MustCompile(`resource\s+"([^"]+)"\s+"[^"]+"\s*\{`)
	nameAttr     = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
)

// declaredNames reads the name of every resource of one type out of Terraform
// source, so the expected set of queues and topics stays in the files that
// provision them instead of being copied in here.
func declaredNames(resourceType, source string) []string {
	var out []string
	heads := resourceHead.FindAllStringSubmatchIndex(source, -1)
	for i, head := range heads {
		if source[head[2]:head[3]] != resourceType {
			continue
		}
		end := len(source)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		if found := nameAttr.FindStringSubmatch(source[head[1]:end]); found != nil {
			out = append(out, found[1])
		}
	}
	return out
}

// alertLine matches the name of one alert rule in the versioned file. The file
// is YAML and this reads it with the standard library alone, which the rest of
// the verifier already commits to: an `alert:` key is one rule, and the name
// is the rest of the line.
var alertLine = regexp.MustCompile(`(?m)^\s*-\s*alert:\s*(\S+)\s*$`)

// declaredAlerts reads the name of every alert rule the versioned file
// declares, in the order of the file, so the expected set stays in the file
// the Prometheus loads instead of being copied in here.
func declaredAlerts(source string) []string {
	var out []string
	for _, found := range alertLine.FindAllStringSubmatch(source, -1) {
		out = append(out, found[1])
	}
	return out
}

// missingRules names every alert the versioned file declares and the
// Prometheus did not load. A rule file that failed to parse leaves the
// Prometheus with the previous set, and this is what says so.
func missingRules(declared, loaded []string) []string {
	var out []string
	for _, name := range declared {
		if !slices.Contains(loaded, name) {
			out = append(out, fmt.Sprintf("alert %s is declared in the versioned rules and the Prometheus did not load it", name))
		}
	}
	return out
}

// compareDashboard names a dashboard the Grafana does not have as a provisioned
// one. The search is by title and answers every kind of hit, so the type is
// what tells a dashboard from a folder of the same name.
func compareDashboard(title string, found []dashboardHit) []string {
	for _, hit := range found {
		if hit.Title == title && hit.Type == "dash-db" {
			return nil
		}
	}
	return []string{fmt.Sprintf("dashboard %q is versioned and the Grafana does not have it provisioned", title)}
}

// firstLine is the first line of what a command printed.
//
// A Compose service answers with one container id per replica, and the question
// asked of it is about the image they all share, so the first one answers for
// them. It also trims a failure to one line: a message broken over several lines
// is one finding read as many.
func firstLine(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if i := strings.IndexByte(trimmed, '\n'); i >= 0 {
		return strings.TrimSpace(trimmed[:i])
	}
	return trimmed
}

var copyLine = regexp.MustCompile(`(?mi)^\s*COPY\s+(.*)$`)

// notTests excludes the test files from a set of paths, in the pathspec form git
// takes.
const notTests = ":(exclude)**/*_test.go"

// imageSources reads the paths a Dockerfile copies in, so the image is compared
// against the commit of what it actually holds.
//
// A build whose layers are all cached keeps the Created of the cached image, and
// that is correct: the image is only rebuilt when its content would differ. So
// the comparison has to be against the last commit that could have changed that
// content, not against the newest commit of the tree — one touching the README
// or the runner changes nothing the image holds.
//
// The test files are excluded for the same reason. They live under a path the
// recipe copies, but only the build stage ever sees them: the image that runs
// carries the binary alone, and a binary built from the same production source
// is byte for byte the one already there.
//
// A `COPY --from` names a stage of the build, not a path of the working tree, so
// that line is dropped. Every other flag is stripped on its own and the paths
// behind it are kept: dropping the whole line for any `--` would take a real path
// out of the dated set for something as ordinary as `COPY --chown`.
func imageSources(dockerfile string) []string {
	var out []string
	for _, found := range copyLine.FindAllStringSubmatch(dockerfile, -1) {
		fields, fromStage := stripCopyFlags(strings.Fields(found[1]))
		if fromStage || len(fields) < 2 {
			continue
		}
		out = append(out, fields[:len(fields)-1]...)
	}
	if len(out) == 0 {
		return nil
	}
	return append(out, notTests)
}

// stripCopyFlags drops the leading flags of a COPY and reports whether one of them
// was `--from`, which makes the line name a stage of the build instead of paths.
func stripCopyFlags(fields []string) ([]string, bool) {
	fromStage := false
	for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		fromStage = fromStage || strings.HasPrefix(fields[0], "--from")
		fields = fields[1:]
	}
	return fields, fromStage
}
