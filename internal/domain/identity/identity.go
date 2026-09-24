package identity

// Every identifier is its own type, so the compiler refuses a wallet where a
// player is expected. The zero value of all of them is invalid, and the parse
// at the border is the only constructor: a bare string does not get in.
//
// Wallet, player, transaction and ledger entry are ours, in canonical UUID.
// Provider, external id, round, game and the idempotency key belong to the
// provider and are kept as they arrived.

type WalletID struct{ canonical }

func ParseWalletID(text string) (WalletID, error) {
	parsed, err := parseCanonical(text)
	if err != nil {
		return WalletID{}, err
	}
	return WalletID{canonical: parsed}, nil
}

type PlayerID struct{ canonical }

func ParsePlayerID(text string) (PlayerID, error) {
	parsed, err := parseCanonical(text)
	if err != nil {
		return PlayerID{}, err
	}
	return PlayerID{canonical: parsed}, nil
}

type TransactionID struct{ canonical }

func ParseTransactionID(text string) (TransactionID, error) {
	parsed, err := parseCanonical(text)
	if err != nil {
		return TransactionID{}, err
	}
	return TransactionID{canonical: parsed}, nil
}

type LedgerEntryID struct{ canonical }

func ParseLedgerEntryID(text string) (LedgerEntryID, error) {
	parsed, err := parseCanonical(text)
	if err != nil {
		return LedgerEntryID{}, err
	}
	return LedgerEntryID{canonical: parsed}, nil
}

type ProviderID struct{ token }

func ParseProviderID(text string) (ProviderID, error) {
	parsed, err := parseToken(text)
	if err != nil {
		return ProviderID{}, err
	}
	return ProviderID{token: parsed}, nil
}

type ExternalTransactionID struct{ token }

func ParseExternalTransactionID(text string) (ExternalTransactionID, error) {
	parsed, err := parseToken(text)
	if err != nil {
		return ExternalTransactionID{}, err
	}
	return ExternalTransactionID{token: parsed}, nil
}

type RoundID struct{ token }

func ParseRoundID(text string) (RoundID, error) {
	parsed, err := parseToken(text)
	if err != nil {
		return RoundID{}, err
	}
	return RoundID{token: parsed}, nil
}

type GameID struct{ token }

func ParseGameID(text string) (GameID, error) {
	parsed, err := parseToken(text)
	if err != nil {
		return GameID{}, err
	}
	return GameID{token: parsed}, nil
}

type IdempotencyKey struct{ token }

func ParseIdempotencyKey(text string) (IdempotencyKey, error) {
	parsed, err := parseToken(text)
	if err != nil {
		return IdempotencyKey{}, err
	}
	return IdempotencyKey{token: parsed}, nil
}
