package mint

import "testing"

func TestWalletID_mintsADistinctIdentityEveryTime(t *testing.T) {
	t.Parallel()
	first, err := UUIDv7{}.WalletID()
	if err != nil {
		t.Fatalf("WalletID = %v, want nil", err)
	}
	second, err := UUIDv7{}.WalletID()
	if err != nil {
		t.Fatalf("WalletID = %v, want nil", err)
	}
	if first == second {
		t.Fatalf("two mints answered %s twice, want distinct identities", first)
	}
	if first.IsZero() {
		t.Fatalf("minted identity is zero, want a usable one")
	}
}

func TestTransactionID_mintsAUsableIdentity(t *testing.T) {
	t.Parallel()
	id, err := UUIDv7{}.TransactionID()
	if err != nil {
		t.Fatalf("TransactionID = %v, want nil", err)
	}
	if id.IsZero() {
		t.Fatalf("minted identity is zero, want a usable one")
	}
}

func TestEntryID_mintsAUsableIdentity(t *testing.T) {
	t.Parallel()
	id, err := UUIDv7{}.EntryID()
	if err != nil {
		t.Fatalf("EntryID = %v, want nil", err)
	}
	if id.IsZero() {
		t.Fatalf("minted identity is zero, want a usable one")
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
