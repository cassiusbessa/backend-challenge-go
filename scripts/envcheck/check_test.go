package main

import (
	"strings"
	"testing"
	"time"
)

// The flag arrives concatenated, so it is cast to text and reads `true` or
// `false`. The bare-column form has a case of its own because psql prints that one
// whenever the flag is selected on its own.
func TestParseSchemaRow_readsACleanVersion(t *testing.T) {
	t.Parallel()
	state, err := parseSchemaRow("junglegaming", "6|false\n")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if state.version != "6" {
		t.Errorf("version = %q, want 6", state.version)
	}
	if state.dirty {
		t.Errorf("dirty = %v, want false", state.dirty)
	}
	if !state.present {
		t.Errorf("present = %v, want true", state.present)
	}
}

func TestParseSchemaRow_readsAVersionLeftDirty(t *testing.T) {
	t.Parallel()
	state, err := parseSchemaRow("junglegaming", "6|true")
	if err != nil {
		t.Fatalf("parse of a dirty row: err = %v, want nil", err)
	}
	if !state.dirty {
		t.Errorf("dirty on a true row = %v, want true", state.dirty)
	}
}

func TestParseSchemaRow_readsTheFlagInTheFormABareColumnPrints(t *testing.T) {
	t.Parallel()
	dirty, err := parseSchemaRow("junglegaming", "6|t")
	if err != nil {
		t.Fatalf("parse of a t row: err = %v, want nil", err)
	}
	if !dirty.dirty {
		t.Errorf("dirty on a t row = %v, want true", dirty.dirty)
	}
	clean, err := parseSchemaRow("junglegaming", "6|f")
	if err != nil {
		t.Fatalf("parse of an f row: err = %v, want nil", err)
	}
	if clean.dirty {
		t.Errorf("dirty on an f row = %v, want false", clean.dirty)
	}
}

func TestParseSchemaRow_refusesADirtyFlagItCannotRead(t *testing.T) {
	t.Parallel()
	if _, err := parseSchemaRow("junglegaming", "6|maybe"); err == nil {
		t.Errorf("err on an unreadable dirty flag = %v, want one", err)
	}
}

func TestParseSchemaRow_readsAnEmptyRowAsNoMigrationApplied(t *testing.T) {
	t.Parallel()
	state, err := parseSchemaRow("junglegaming_test", "")
	if err != nil {
		t.Fatalf("parse of an empty row: err = %v, want nil", err)
	}
	if state.version != noSchema {
		t.Errorf("version of an empty row = %q, want %q", state.version, noSchema)
	}
	if !state.present {
		t.Errorf("present on an empty row = %v, want true", state.present)
	}
}

func TestParseSchemaRow_refusesARowOfThreeFields(t *testing.T) {
	t.Parallel()
	if _, err := parseSchemaRow("junglegaming", "6|f|extra"); err == nil {
		t.Errorf("err on a row of three fields = %v, want one", err)
	}
}

func TestCompareSchema_staysQuietWhenBothAreAtTheSameVersion(t *testing.T) {
	t.Parallel()
	got := compareSchema([]schemaState{
		{name: "junglegaming", present: true, version: "6"},
		{name: "junglegaming_test", present: true, version: "6"},
	})
	if len(got) != 0 {
		t.Errorf("findings for two databases at the same version = %v, want none", got)
	}
}

func TestCompareSchema_namesADatabaseThatIsMissing(t *testing.T) {
	t.Parallel()
	got := compareSchema([]schemaState{
		{name: "junglegaming", present: true, version: "6"},
		{name: "junglegaming_test"},
	})
	if len(got) != 1 || !strings.Contains(got[0], "junglegaming_test does not exist") {
		t.Errorf("findings for a missing database = %v, want one naming it", got)
	}
}

func TestCompareSchema_namesADatabaseLeftDirty(t *testing.T) {
	t.Parallel()
	got := compareSchema([]schemaState{
		{name: "junglegaming_test", present: true, version: "6", dirty: true},
	})
	if len(got) != 1 || !strings.Contains(got[0], "dirty") {
		t.Errorf("findings for a dirty database = %v, want one saying dirty", got)
	}
}

func TestCompareSchema_namesBothDatabasesWhenOneIsBehind(t *testing.T) {
	t.Parallel()
	got := compareSchema([]schemaState{
		{name: "junglegaming", present: true, version: "6"},
		{name: "junglegaming_test", present: true, version: "5"},
	})
	if len(got) != 1 {
		t.Fatalf("findings for two versions = %v, want exactly one", got)
	}
	if !strings.Contains(got[0], "junglegaming at 6") || !strings.Contains(got[0], "junglegaming_test at 5") {
		t.Errorf("the finding for two versions = %q, want both databases and both versions", got[0])
	}
}

func TestCompareSchema_namesADatabaseWithNoMigrationAtAll(t *testing.T) {
	t.Parallel()
	got := compareSchema([]schemaState{
		{name: "junglegaming_test", present: true, version: noSchema},
	})
	if len(got) != 1 || !strings.Contains(got[0], "no migration applied") {
		t.Errorf("findings for an empty database = %v, want one saying no migration", got)
	}
}

func TestDisagreeingVersions_staysQuietUntilTwoAreApplied(t *testing.T) {
	t.Parallel()
	t.Run("only one database applied", func(t *testing.T) {
		got := disagreeingVersions([]schemaState{
			{name: "junglegaming", present: true, version: "6"},
			{name: "junglegaming_test"},
		})
		if len(got) != 0 {
			t.Errorf("findings with a single applied database = %v, want none: the missing one is already named", got)
		}
	})

	t.Run("an unapplied database is not behind", func(t *testing.T) {
		got := disagreeingVersions([]schemaState{
			{name: "junglegaming", present: true, version: "6"},
			{name: "junglegaming_test", present: true, version: noSchema},
		})
		if len(got) != 0 {
			t.Errorf("findings against an unmigrated database = %v, want none", got)
		}
	})
}

func TestCompareLifespan_namesTheRealmAndBothValues(t *testing.T) {
	t.Parallel()
	t.Run("the realm issues what the file declares", func(t *testing.T) {
		if got := compareLifespan("junglegaming", 300, 300); len(got) != 0 {
			t.Errorf("findings for a realm that issues what is declared = %v, want none", got)
		}
	})

	t.Run("an interrupted suite left it shortened", func(t *testing.T) {
		got := compareLifespan("junglegaming", 300, 1)
		if len(got) != 1 {
			t.Fatalf("findings for a shortened lifespan = %v, want exactly one", got)
		}
		for _, want := range []string{"junglegaming", "1s", "300s"} {
			if !strings.Contains(got[0], want) {
				t.Errorf("the finding = %q, want it to carry %q", got[0], want)
			}
		}
	})
}

func TestMissingBroker_namesOnlyWhatTheBrokerDoesNotHave(t *testing.T) {
	t.Parallel()
	queues := []string{
		"http://localhost:4566/000000000000/wager-transactions.fifo",
		"http://localhost:4566/000000000000/wager-transactions-dlq.fifo",
	}
	t.Run("every declared queue provisioned", func(t *testing.T) {
		want := []string{"wager-transactions.fifo", "wager-transactions-dlq.fifo"}
		if got := missingBroker("queue", want, queues); len(got) != 0 {
			t.Errorf("findings for every declared queue provisioned = %v, want none", got)
		}
	})

	t.Run("one declared queue absent", func(t *testing.T) {
		want := []string{"wager-transactions.fifo", "wager-retry.fifo"}
		got := missingBroker("queue", want, queues)
		if len(got) != 1 || !strings.Contains(got[0], "wager-retry.fifo") {
			t.Errorf("findings for an absent queue = %v, want one naming it", got)
		}
	})

	t.Run("a topic recognised inside its arn", func(t *testing.T) {
		have := []string{"arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"}
		if got := missingBroker("topic", []string{"wallet-events.fifo"}, have); len(got) != 0 {
			t.Errorf("findings for a provisioned topic = %v, want none", got)
		}
	})

	t.Run("a name that only shares a suffix is not a match", func(t *testing.T) {
		have := []string{"http://localhost:4566/000000000000/other-wager-transactions.fifo"}
		got := missingBroker("queue", []string{"wager-transactions.fifo"}, have)
		if len(got) != 1 {
			t.Errorf("findings for a partial suffix = %v, want one: the separator has to be there too", got)
		}
	})
}

func TestStaleImage_comparesTheBuildWithTheCommit(t *testing.T) {
	t.Parallel()
	commit := time.Date(2026, time.September, 26, 12, 0, 0, 0, time.UTC)

	t.Run("built after the commit", func(t *testing.T) {
		built := commit.Add(time.Second)
		if got := staleImage("wager", built, commit); len(got) != 0 {
			t.Errorf("findings for an image built after the commit = %v, want none", got)
		}
	})

	t.Run("built at the very instant of the commit", func(t *testing.T) {
		if got := staleImage("wager", commit, commit); len(got) != 0 {
			t.Errorf("findings at the exact commit instant = %v, want none", got)
		}
	})

	t.Run("built one second before the commit", func(t *testing.T) {
		built := commit.Add(-time.Second)
		got := staleImage("wager", built, commit)
		if len(got) != 1 {
			t.Fatalf("findings for a stale image = %v, want exactly one", got)
		}
		if !strings.Contains(got[0], "wager") || !strings.Contains(got[0], "2026-09-26T12:00:00Z") {
			t.Errorf("the finding = %q, want the service and the commit instant", got[0])
		}
	})
}

func TestDeclaredNames_readsTheNameOfEachResourceOfOneType(t *testing.T) {
	t.Parallel()
	source := `
resource "aws_sqs_queue" "wager_transactions_dlq" {
  name                        = "wager-transactions-dlq.fifo"
  fifo_queue                  = true
}

resource "aws_sqs_queue" "wager_transactions" {
  name                       = "wager-transactions.fifo"
  visibility_timeout_seconds = 30

  redrive_policy = jsonencode({
    maxReceiveCount = 15
  })
}

resource "aws_sns_topic" "wallet_events" {
  name       = "wallet-events.fifo"
  fifo_topic = true
}
`
	t.Run("the queues", func(t *testing.T) {
		got := declaredNames("aws_sqs_queue", source)
		want := []string{"wager-transactions-dlq.fifo", "wager-transactions.fifo"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("got = %v, want %v", got, want)
		}
	})

	t.Run("the topic, without the queues", func(t *testing.T) {
		got := declaredNames("aws_sns_topic", source)
		if len(got) != 1 || got[0] != "wallet-events.fifo" {
			t.Errorf("topics = %v, want only wallet-events.fifo", got)
		}
	})

	t.Run("a type nothing declares", func(t *testing.T) {
		if got := declaredNames("aws_iam_role", source); len(got) != 0 {
			t.Errorf("names for an undeclared type = %v, want none", got)
		}
	})
}

func TestFirstLine_answersForEveryReplicaWithTheFirstOne(t *testing.T) {
	t.Parallel()
	t.Run("several containers of one service", func(t *testing.T) {
		got := firstLine("aaa111\nbbb222\nccc333\n")
		if got != "aaa111" {
			t.Errorf("first of three container ids = %q, want aaa111", got)
		}
	})

	t.Run("a single container", func(t *testing.T) {
		if got := firstLine("  aaa111  \n"); got != "aaa111" {
			t.Errorf("a single container id = %q, want aaa111", got)
		}
	})

	t.Run("a service that is not running", func(t *testing.T) {
		if got := firstLine("\n  \n"); got != "" {
			t.Errorf("what a stopped service answers = %q, want the empty string", got)
		}
	})
}

// recipeOfTheImage is the shape of the versioned recipe: a build stage that copies
// the working tree in, and a runtime stage that copies only the binary out of it.
const recipeOfTheImage = `
FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/wager ./cmd/wager

FROM alpine:3.22
COPY --from=build /out/wager /wager
ENTRYPOINT ["/wager"]
`

func TestImageSources_readsEveryPathOfTheWorkingTreeMinusTheTests(t *testing.T) {
	t.Parallel()
	got := strings.Join(imageSources(recipeOfTheImage), " ")
	want := "go.mod go.sum cmd internal " + notTests
	if got != want {
		t.Errorf("sources = %q, want %q", got, want)
	}
}

func TestImageSources_excludesTheTestsRatherThanOmittingThem(t *testing.T) {
	t.Parallel()
	got := imageSources(recipeOfTheImage)
	if got[len(got)-1] != notTests {
		t.Errorf("last pathspec = %q, want the exclusion %q: only the build stage sees a test file", got[len(got)-1], notTests)
	}
}

func TestImageSources_leavesOutAStageOfTheBuild(t *testing.T) {
	t.Parallel()
	for _, each := range imageSources(recipeOfTheImage) {
		if each == "/out/wager" {
			t.Errorf("sources = %v, want /out/wager left out: COPY --from names a build stage", imageSources(recipeOfTheImage))
		}
	}
}

func TestImageSources_keepsThePathsBehindAFlagThatIsNotAStage(t *testing.T) {
	t.Parallel()
	got := strings.Join(imageSources("COPY --chown=nonroot:nonroot deploy/local /etc/wager\n"), " ")
	want := "deploy/local " + notTests
	if got != want {
		t.Errorf("sources behind a --chown = %q, want %q", got, want)
	}
}

func TestImageSources_readsAStageNamedAfterAnotherFlagAsAStage(t *testing.T) {
	t.Parallel()
	if got := imageSources("COPY --chown=root --from=build /out/wager /wager\n"); len(got) != 0 {
		t.Errorf("sources of a flagged stage copy = %v, want none", got)
	}
}

func TestImageSources_answersNothingForARecipeThatCopiesNothing(t *testing.T) {
	t.Parallel()
	if got := imageSources("FROM alpine:3.22\nENTRYPOINT [\"/bin/sh\"]\n"); len(got) != 0 {
		t.Errorf("sources of a recipe without COPY = %v, want none", got)
	}
}

func TestDeclaredMigration_readsTheHighestVersionTheFilesDeclare(t *testing.T) {
	t.Parallel()
	t.Run("the highest of several, without the leading zeros", func(t *testing.T) {
		got, err := declaredMigration([]string{
			"deploy/migrations/000001_financial_schema.up.sql",
			"deploy/migrations/000006_wager_inbox.up.sql",
			"deploy/migrations/000004_outbox_publish_order.up.sql",
		})
		if err != nil {
			t.Fatalf("err on a set of three migrations = %v, want nil", err)
		}
		if got != "6" {
			t.Errorf("declared version = %q, want 6", got)
		}
	})

	t.Run("a version of two digits is not compared as text", func(t *testing.T) {
		got, err := declaredMigration([]string{"000009_nine.up.sql", "000010_ten.up.sql"})
		if err != nil {
			t.Fatalf("err on a set of two digits = %v, want nil", err)
		}
		if got != "10" {
			t.Errorf("declared version of a set of two digits = %q, want 10", got)
		}
	})

	t.Run("a set that names no version", func(t *testing.T) {
		if _, err := declaredMigration([]string{"README.md"}); err == nil {
			t.Errorf("err on a set without a migration = %v, want one", err)
		}
	})
}

func TestCompareDeclared_namesTheDatabaseBehindTheMigrations(t *testing.T) {
	t.Parallel()
	t.Run("both at the version the files declare", func(t *testing.T) {
		got := compareDeclared("6", []schemaState{
			{name: "junglegaming", present: true, version: "6"},
			{name: "junglegaming_test", present: true, version: "6"},
		})
		if len(got) != 0 {
			t.Errorf("findings when both are current = %v, want none", got)
		}
	})

	t.Run("a migration applied to neither database", func(t *testing.T) {
		got := compareDeclared("7", []schemaState{
			{name: "junglegaming", present: true, version: "6"},
			{name: "junglegaming_test", present: true, version: "6"},
		})
		if len(got) != 2 {
			t.Fatalf("findings when the migration reached neither = %v, want two", got)
		}
		if !strings.Contains(got[0], "declare 7") {
			t.Errorf("finding of a database left behind = %q, want the declared version in it", got[0])
		}
	})

	t.Run("a state another comparison already named", func(t *testing.T) {
		got := compareDeclared("6", []schemaState{
			{name: "junglegaming", present: false},
			{name: "junglegaming_test", present: true, version: noSchema},
		})
		if len(got) != 0 {
			t.Errorf("findings for states compareSchema already names = %v, want none", got)
		}
	})
}

func TestDeclaredAlerts_readsTheNameOfEveryRuleInTheOrderOfTheFile(t *testing.T) {
	t.Parallel()
	source := `
groups:
  - name: settlement
    rules:
      - alert: ReconciliationDivergenceFound
        expr: sum(increase(wager_reconciliation_divergences_total[15m])) > 0
      - record: job:up
        expr: up
      - alert: OutboxOldestPendingTooOld
        expr: max(wager_outbox_oldest_pending_age_seconds) > 30
        for: 1m
`
	got := strings.Join(declaredAlerts(source), ",")
	if got != "ReconciliationDivergenceFound,OutboxOldestPendingTooOld" {
		t.Errorf("alerts = %q, want the two alerts and not the recording rule", got)
	}
	if got := declaredAlerts("groups: []\n"); len(got) != 0 {
		t.Errorf("alerts of a file without one = %v, want none", got)
	}
}

// The Prometheus reads the name as a YAML scalar, so a name in quotes is the
// same name without them, and a comment after it is not part of it. Reading
// the quotes into the name would report a loaded rule as missing.
func TestDeclaredAlerts_readsTheNameAsTheScalarThePrometheusReads(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
	}{
		{name: "plain", line: "  - alert: OutboxOldestPendingTooOld"},
		{name: "double quoted", line: `  - alert: "OutboxOldestPendingTooOld"`},
		{name: "single quoted", line: "  - alert: 'OutboxOldestPendingTooOld'"},
		{name: "followed by a comment", line: "  - alert: OutboxOldestPendingTooOld # the lease absorbs 30s"},
		{name: "quoted and followed by a comment", line: `  - alert: "OutboxOldestPendingTooOld"  # quoted`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := declaredAlerts("groups:\n  - name: settlement\n    rules:\n" + tc.line + "\n")
			if len(got) != 1 || got[0] != "OutboxOldestPendingTooOld" {
				t.Errorf("alerts of %q = %q, want the name alone", tc.line, got)
			}
		})
	}
}

func TestMissingRules_namesOnlyWhatThePrometheusDidNotLoad(t *testing.T) {
	t.Parallel()
	declared := []string{"ReconciliationDivergenceFound", "OutboxOldestPendingTooOld"}
	t.Run("both loaded", func(t *testing.T) {
		if got := missingRules(declared, []string{"OutboxOldestPendingTooOld", "ReconciliationDivergenceFound"}); len(got) != 0 {
			t.Errorf("findings with both loaded = %v, want none", got)
		}
	})

	t.Run("one left behind", func(t *testing.T) {
		got := missingRules(declared, []string{"ReconciliationDivergenceFound"})
		if len(got) != 1 || !strings.Contains(got[0], "OutboxOldestPendingTooOld") {
			t.Errorf("findings with one rule missing = %v, want one naming it", got)
		}
	})

	t.Run("none loaded", func(t *testing.T) {
		if got := missingRules(declared, nil); len(got) != 2 {
			t.Errorf("findings with nothing loaded = %v, want both named", got)
		}
	})
}

func TestCompareDashboard_acceptsOnlyADashboardOfThatTitle(t *testing.T) {
	t.Parallel()
	t.Run("the dashboard is there", func(t *testing.T) {
		found := []dashboardHit{{Title: "Liquidação", Type: "dash-db", UID: "liquidacao"}}
		if got := compareDashboard("Liquidação", found); len(got) != 0 {
			t.Errorf("findings with the dashboard provisioned = %v, want none", got)
		}
	})

	t.Run("only a folder of that name", func(t *testing.T) {
		found := []dashboardHit{{Title: "Liquidação", Type: "dash-folder"}}
		got := compareDashboard("Liquidação", found)
		if len(got) != 1 || !strings.Contains(got[0], "Liquidação") {
			t.Errorf("findings with a folder of the same name = %v, want one naming the dashboard", got)
		}
	})

	t.Run("nothing found", func(t *testing.T) {
		if got := compareDashboard("Liquidação", nil); len(got) != 1 {
			t.Errorf("findings with nothing found = %v, want one", got)
		}
	})
}

// A COPY that carries only flags leaves nothing behind them, and the strip
// stops there instead of reading past the end of the line.
func TestStripCopyFlags_stopsAtTheEndOfALineThatIsOnlyFlags(t *testing.T) {
	t.Parallel()
	got, fromStage := stripCopyFlags([]string{"--chown=nonroot:nonroot", "--chmod=0755"})
	if len(got) != 0 || fromStage {
		t.Errorf("stripCopyFlags of flags alone = %v, %t, want nothing left and no stage", got, fromStage)
	}
}

// Nothing declared is the sentinel below zero, so zero is the boundary: a
// migration numbered 0 still names a version.
func TestDeclaredMigration_readsVersionZeroAsAVersion(t *testing.T) {
	t.Parallel()
	got, err := declaredMigration([]string{"deploy/migrations/000000_init.up.sql"})
	if err != nil {
		t.Fatalf("declaredMigration of version zero: err = %v, want nil", err)
	}
	if got != "0" {
		t.Errorf("declared version = %q, want 0", got)
	}
}
