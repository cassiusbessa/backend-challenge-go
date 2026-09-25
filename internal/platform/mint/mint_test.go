package mint

import "testing"

func TestWalletID_mintsADistinctIdentityEveryTime(t *testing.T) {
	t.Parallel()
	first, err := UUIDv7{}.WalletID()
	if err != nil {
		t.Fatalf("first WalletID = %v, want nil", err)
	}
	second, err := UUIDv7{}.WalletID()
	if err != nil {
		t.Fatalf("second WalletID = %v, want nil", err)
	}
	if first == second {
		t.Fatalf("two mints answered %s twice, want distinct identities", first)
	}
	if first.IsZero() {
		t.Fatalf("minted wallet identity = %s, want a usable one", first)
	}
}

func TestTransactionID_mintsAUsableIdentity(t *testing.T) {
	t.Parallel()
	id, err := UUIDv7{}.TransactionID()
	if err != nil {
		t.Fatalf("TransactionID = %v, want nil", err)
	}
	if id.IsZero() {
		t.Fatalf("minted transaction identity = %s, want a usable one", id)
	}
}

func TestEntryID_mintsAUsableIdentity(t *testing.T) {
	t.Parallel()
	id, err := UUIDv7{}.EntryID()
	if err != nil {
		t.Fatalf("EntryID = %v, want nil", err)
	}
	if id.IsZero() {
		t.Fatalf("minted entry identity = %s, want a usable one", id)
	}
}

func TestEventID_mintsAUsableIdentity(t *testing.T) {
	t.Parallel()
	id, err := UUIDv7{}.EventID()
	if err != nil {
		t.Fatalf("EventID = %v, want nil", err)
	}
	if id.IsZero() {
		t.Fatalf("minted event identity = %s, want a usable one", id)
	}
}

// Version 7 puts the time first, so a later mint sorts after an earlier one and
// the primary key index stays local instead of scattering writes.
func TestNext_ordersLaterMintsAfterEarlierOnes(t *testing.T) {
	t.Parallel()
	first := next()
	second := next()
	if first >= second {
		t.Fatalf("mints = %s then %s, want the later one to sort after", first, second)
	}
}
