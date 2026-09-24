package identity

import (
	"errors"
	"fmt"
	"uuid"
)

var ErrInvalidUUID = errors.New("identity: identifier is not in the UUID format")

// canonical wraps the UUID of the standard library, available since Go 1.27.
//
// The domain never mints an identifier: it takes the text the border already
// resolved and keeps the sixteen bytes. String always answers in the canonical
// lowercase form, which is what the idempotency hash consumes with no extra
// normalization step.
type canonical struct {
	value uuid.UUID
}

func parseCanonical(text string) (canonical, error) {
	parsed, err := uuid.Parse(text)
	if err != nil {
		// Two %w keep both the domain sentinel and the standard library detail
		// matchable, on one line. errors.Join would break the log line in two.
		return canonical{}, fmt.Errorf("%w: %w", ErrInvalidUUID, err)
	}
	return canonical{value: parsed}, nil
}

func (c canonical) String() string {
	return c.value.String()
}

// IsZero reports whether the identifier is absent. The nil UUID counts as
// absent: it is the zero value of the type, not an identity.
func (c canonical) IsZero() bool {
	return c.value == uuid.Nil()
}
