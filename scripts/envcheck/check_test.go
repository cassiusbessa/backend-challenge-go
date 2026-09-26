package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseSchemaRow_readsWhatPsqlPrintsForEachState(t *testing.T) {
	t.Parallel()
	// The flag arrives concatenated, so it is cast to text and reads `true` or
	// `false`. The bare-column form is asserted below because psql prints that
	// one whenever the flag is selected on its own.
	t.Run("a clean version", func(t *testing.T) {
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
	})

	t.Run("a version left dirty", func(t *testing.T) {
		state, err := parseSchemaRow("junglegaming", "6|true")
		if err != nil {
			t.Fatalf("parse of a dirty row: err = %v, want nil", err)
		}
		if !state.dirty {
			t.Errorf("dirty on a true row = %v, want true", state.dirty)
		}
	})

	t.Run("the flag as a bare column prints it", func(t *testing.T) {
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
	})

	t.Run("a dirty flag it cannot read", func(t *testing.T) {
		if _, err := parseSchemaRow("junglegaming", "6|maybe"); err == nil {
			t.Errorf("err on an unreadable dirty flag = %v, want one", err)
		}
	})

	t.Run("a database with no migration applied", func(t *testing.T) {
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
	})

	t.Run("a row it cannot read", func(t *testing.T) {
		if _, err := parseSchemaRow("junglegaming", "6|f|extra"); err == nil {
			t.Errorf("err on a row of three fields = %v, want one", err)
		}
	})
}

func TestCompareSchema_namesTheDatabaseThatIsNotAsDeclared(t *testing.T) {
	t.Parallel()
	t.Run("both applied at the same version", func(t *testing.T) {
		got := compareSchema([]schemaState{
			{name: "junglegaming", present: true, version: "6"},
			{name: "junglegaming_test", present: true, version: "6"},
		})
		if len(got) != 0 {
			t.Errorf("findings for two databases at the same version = %v, want none", got)
		}
	})

	t.Run("one of them missing", func(t *testing.T) {
		got := compareSchema([]schemaState{
			{name: "junglegaming", present: true, version: "6"},
			{name: "junglegaming_test"},
		})
		if len(got) != 1 || !strings.Contains(got[0], "junglegaming_test does not exist") {
			t.Errorf("findings for a missing database = %v, want one naming it", got)
		}
	})

	t.Run("one of them left dirty", func(t *testing.T) {
		got := compareSchema([]schemaState{
			{name: "junglegaming_test", present: true, version: "6", dirty: true},
		})
		if len(got) != 1 || !strings.Contains(got[0], "dirty") {
			t.Errorf("findings for a dirty database = %v, want one saying dirty", got)
		}
	})

	t.Run("one of them behind the other", func(t *testing.T) {
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
	})

	t.Run("a database with no migration at all", func(t *testing.T) {
		got := compareSchema([]schemaState{
			{name: "junglegaming_test", present: true, version: noSchema},
		})
		if len(got) != 1 || !strings.Contains(got[0], "no migration applied") {
			t.Errorf("findings for an empty database = %v, want one saying no migration", got)
		}
	})
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

func TestImageSources_readsWhatTheRecipeCopiesIn(t *testing.T) {
	t.Parallel()
	recipe := `
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
	t.Run("every path of the working tree, minus the tests", func(t *testing.T) {
		got := strings.Join(imageSources(recipe), " ")
		want := "go.mod go.sum cmd internal " + notTests
		if got != want {
			t.Errorf("sources = %q, want %q", got, want)
		}
	})

	t.Run("the tests are excluded, not merely absent", func(t *testing.T) {
		got := imageSources(recipe)
		if got[len(got)-1] != notTests {
			t.Errorf("last pathspec = %q, want the exclusion %q: only the build stage sees a test file", got[len(got)-1], notTests)
		}
	})

	t.Run("a stage of the build is not a path", func(t *testing.T) {
		got := imageSources(recipe)
		for _, each := range got {
			if each == "/out/wager" {
				t.Errorf("sources = %v, want /out/wager left out: COPY --from names a build stage", got)
			}
		}
	})

	t.Run("a recipe that copies nothing", func(t *testing.T) {
		if got := imageSources("FROM alpine:3.22\nENTRYPOINT [\"/bin/sh\"]\n"); len(got) != 0 {
			t.Errorf("sources of a recipe without COPY = %v, want none", got)
		}
	})
}
