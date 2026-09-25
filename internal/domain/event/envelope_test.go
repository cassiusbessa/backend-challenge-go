package event

import (
	"errors"
	"testing"
	"time"
)

// outcome is one event of a settled operation, which is the envelope the cases
// about the wire form read.
func outcome(t *testing.T) Envelope {
	t.Helper()
	built, err := NewProcessed(spec(t), processedBet(t))
	if err != nil {
		t.Fatalf("NewProcessed = %v, want nil", err)
	}
	return built
}

func TestNew_fixesTheTypeAndTheVersionAndKeepsTheInstantInUTC(t *testing.T) {
	t.Parallel()
	saoPaulo := time.FixedZone("-03", -3*60*60)
	local := spec(t)
	local.At = at.In(saoPaulo)
	built, err := New(local, BalanceChangedData{})
	if err != nil {
		t.Fatalf("New = %v, want nil", err)
	}
	if built.Version() != 1 {
		t.Fatalf("version = %d, want 1", built.Version())
	}
	if built.Type() != TypeBalanceChanged {
		t.Fatalf("type = %s, want %s", built.Type(), TypeBalanceChanged)
	}
	if built.OccurredAt().Location() != time.UTC {
		t.Fatalf("occurredAt zone = %s, want UTC", built.OccurredAt().Location())
	}
	if !built.OccurredAt().Equal(at) {
		t.Fatalf("occurredAt = %s, want %s", built.OccurredAt(), at)
	}
}

func TestNew_refusesAnEnvelopeThatNamesNoEventWalletInstantOrData(t *testing.T) {
	t.Parallel()
	cases := map[string]Spec{
		"no event id": {AggregateID: walletID(t), At: at},
		"no wallet":   {ID: eventID(t), At: at},
		"no instant":  {ID: eventID(t), AggregateID: walletID(t)},
	}
	for name, incomplete := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(incomplete, BalanceChangedData{}); !errors.Is(err, ErrIncompleteEnvelope) {
				t.Fatalf("New with %s = %v, want ErrIncompleteEnvelope", name, err)
			}
		})
	}
	t.Run("no data", func(t *testing.T) {
		if _, err := New(spec(t), nil); !errors.Is(err, ErrIncompleteEnvelope) {
			t.Fatalf("New with no data = %v, want ErrIncompleteEnvelope", err)
		}
	})
}

func TestMarshal_namesTheEventItsVersionAndTheWalletThatOrdersIt(t *testing.T) {
	t.Parallel()
	out := wireOf(t, outcome(t))
	fields := map[string]any{
		"eventId":     eventUUID,
		"aggregateId": walletUUID,
		"eventType":   string(TypeProcessed),
		"version":     float64(1),
		"occurredAt":  "2026-09-24T12:00:00Z",
	}
	for field, want := range fields {
		if out[field] != want {
			t.Fatalf("%s = %v, want %v", field, out[field], want)
		}
	}
}

func TestMarshal_omitsTheCauseWhenTheOperationHasNoneAndKeepsTheCorrelation(t *testing.T) {
	t.Parallel()
	built := outcome(t)
	uncaused := wireOf(t, built)
	if cause, present := uncaused["causationId"]; present {
		t.Fatalf("causationId of an operation with no cause = %v, want the field omitted", cause)
	}
	if uncaused["correlationId"] != "corr-1" {
		t.Fatalf("correlationId = %v, want corr-1", uncaused["correlationId"])
	}
	caused := marshalWith(t, built, Origin{CorrelationID: "corr-1", CausationID: "msg-1"})
	if caused["causationId"] != "msg-1" {
		t.Fatalf("causationId = %v, want msg-1", caused["causationId"])
	}
}
