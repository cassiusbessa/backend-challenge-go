// Package suiteenv holds what every package of the journey suite needs to reach
// the environment: where the suite lands when nothing says otherwise, how it
// reads an override, and the identifier of a row a case creates.
//
// It carries no build tag, so it is compiled and linted with the rest of the
// module even though only the tagged suites import it.
package suiteenv

import (
	"os"
	"uuid"
)

// DatabaseURLKey is the variable every package of the suite reads for the
// database it connects to.
const DatabaseURLKey = "DATABASE_URL"

// SuiteDatabaseURL is where the suite lands when DatabaseURLKey is unset. It is
// never the database the running application uses: the outbox relay of that
// process scans the whole table every second without filtering by wallet, and it
// publishes the row a case here expects to see dead.
const SuiteDatabaseURL = "postgres://junglegaming:junglegaming@localhost:5432/junglegaming_test?sslmode=disable"

// Or answers the value of key, or fallback when the variable is unset or empty.
// An empty variable falls back because that is what an unset one looks like once
// a shell has exported it.
func Or(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// DatabaseURL answers the database the suite connects to.
func DatabaseURL() string {
	return Or(DatabaseURLKey, SuiteDatabaseURL)
}

// NewID answers the identifier of a row a case creates, ordered by time so the
// rows of one case keep the order it wrote them in.
func NewID() string {
	return uuid.NewV7().String()
}
