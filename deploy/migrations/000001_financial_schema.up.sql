-- As três tabelas financeiras e as invariantes que o PostgreSQL recusa por
-- conta própria. O agregado em Go pede a escrita; o commit só permanece se o
-- banco aceitar. Ver .claude/rules/go-db-invariants.md.

CREATE TABLE wallets (
    id            uuid        PRIMARY KEY,
    player_id     uuid        NOT NULL,
    currency      text        NOT NULL,
    balance_cents bigint      NOT NULL,
    version       bigint      NOT NULL,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL,

    CONSTRAINT wallets_currency_is_iso_4217 CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT wallets_balance_is_not_negative CHECK (balance_cents >= 0),
    CONSTRAINT wallets_version_starts_at_one CHECK (version >= 1),
    CONSTRAINT wallets_one_per_player_and_currency UNIQUE (player_id, currency)
);

CREATE TABLE wager_transactions (
    id                     uuid        PRIMARY KEY,
    kind                   text        NOT NULL,
    player_id              uuid        NOT NULL,
    wallet_id              uuid        NOT NULL REFERENCES wallets (id),
    amount_cents           bigint      NOT NULL,
    currency               text        NOT NULL,

    provider_id            text,
    external_id            text,
    idempotency_key        text,
    body_hash              text,
    round_id               text,
    game_id                text,
    reference_external_id  text,

    status                 text        NOT NULL,
    failure_code           text,
    observed_balance_cents bigint,
    next_attempt_at        timestamptz,
    reference_deadline_at  timestamptz,
    attempt_count          integer     NOT NULL DEFAULT 0,

    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,

    CONSTRAINT wager_transactions_kind_is_known
        CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),

    -- PENDING está fora da lista de propósito: trabalho interrompido só existe
    -- em memória, e o commit grava um status terminal ou a espera.
    CONSTRAINT wager_transactions_status_is_writable
        CHECK (status IN ('PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),

    CONSTRAINT wager_transactions_currency_is_iso_4217 CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT wager_transactions_attempt_count_is_not_negative CHECK (attempt_count >= 0),

    CONSTRAINT wager_transactions_amount_matches_kind CHECK (
        (kind = 'LOSS' AND amount_cents = 0) OR (kind <> 'LOSS' AND amount_cents > 0)
    ),

    CONSTRAINT wager_transactions_external_carries_provider_fields CHECK (
        kind = 'OPENING' OR (
            provider_id IS NOT NULL AND
            external_id IS NOT NULL AND
            idempotency_key IS NOT NULL AND
            body_hash IS NOT NULL AND
            round_id IS NOT NULL AND
            game_id IS NOT NULL
        )
    ),

    CONSTRAINT wager_transactions_opening_is_internal CHECK (
        kind <> 'OPENING' OR (
            provider_id IS NULL AND
            external_id IS NULL AND
            idempotency_key IS NULL AND
            body_hash IS NULL AND
            round_id IS NULL AND
            game_id IS NULL AND
            reference_external_id IS NULL
        )
    ),

    CONSTRAINT wager_transactions_reversal_cites_reference CHECK (
        kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_id IS NOT NULL
    ),

    CONSTRAINT wager_transactions_closed_by_rule_names_failure CHECK (
        status NOT IN ('REJECTED', 'FAILED') OR failure_code IS NOT NULL
    ),

    CONSTRAINT wager_transactions_processed_records_balance CHECK (
        status <> 'PROCESSED' OR observed_balance_cents IS NOT NULL
    ),

    CONSTRAINT wager_transactions_waiting_has_next_attempt CHECK (
        status <> 'PENDING_REFERENCE' OR next_attempt_at IS NOT NULL
    ),

    -- O lançamento referencia a transação com a mesma carteira, moeda e
    -- quantia, e uma chave estrangeira composta precisa desta chave única.
    CONSTRAINT wager_transactions_movement_key UNIQUE (id, wallet_id, currency, amount_cents)
);

CREATE UNIQUE INDEX wager_transactions_one_opening_per_wallet
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';

CREATE UNIQUE INDEX wager_transactions_one_per_provider_and_external_id
    ON wager_transactions (provider_id, external_id) WHERE provider_id IS NOT NULL;

CREATE UNIQUE INDEX wager_transactions_one_per_provider_and_key
    ON wager_transactions (provider_id, idempotency_key) WHERE provider_id IS NOT NULL;

-- Uma reversão PROCESSED por transação citada. O índice é parcial porque a
-- reversão que terminou REJECTED não ocupa a vaga.
CREATE UNIQUE INDEX wager_transactions_one_processed_reversal_per_reference
    ON wager_transactions (provider_id, reference_external_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

CREATE TABLE ledger_entries (
    id                   uuid        PRIMARY KEY,
    wallet_id            uuid        NOT NULL REFERENCES wallets (id),
    transaction_id       uuid        NOT NULL,
    direction            text        NOT NULL,
    amount_cents         bigint      NOT NULL,
    currency             text        NOT NULL,
    balance_before_cents bigint      NOT NULL,
    balance_after_cents  bigint      NOT NULL,
    sequence_number      bigint      NOT NULL,
    created_at           timestamptz NOT NULL,

    CONSTRAINT ledger_entries_direction_is_known CHECK (direction IN ('DEBIT', 'CREDIT')),
    CONSTRAINT ledger_entries_amount_is_positive CHECK (amount_cents > 0),
    CONSTRAINT ledger_entries_balance_after_is_not_negative CHECK (balance_after_cents >= 0),
    CONSTRAINT ledger_entries_sequence_starts_at_one CHECK (sequence_number >= 1),

    CONSTRAINT ledger_entries_direction_moves_the_balance CHECK (
        (direction = 'CREDIT' AND balance_after_cents = balance_before_cents + amount_cents) OR
        (direction = 'DEBIT' AND balance_after_cents = balance_before_cents - amount_cents)
    ),

    CONSTRAINT ledger_entries_one_per_transaction UNIQUE (transaction_id),
    CONSTRAINT ledger_entries_one_sequence_per_wallet UNIQUE (wallet_id, sequence_number),

    CONSTRAINT ledger_entries_match_their_transaction
        FOREIGN KEY (transaction_id, wallet_id, currency, amount_cents)
        REFERENCES wager_transactions (id, wallet_id, currency, amount_cents)
);

-- O ledger só aceita INSERT. O gatilho recusa também o superusuário, que passa
-- por cima de qualquer GRANT.
CREATE FUNCTION ledger_entries_refuse_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger_entries accepts INSERT only'
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER ledger_entries_refuse_update_and_delete
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_entries_refuse_change();

CREATE TRIGGER ledger_entries_refuse_truncate
    BEFORE TRUNCATE ON ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_entries_refuse_change();

-- No commit, o saldo da carteira é o saldo posterior do último lançamento. O
-- gatilho é DEFERRABLE INITIALLY DEFERRED porque no meio da transação SQL os
-- dois divergem: a carteira é escrita antes do lançamento.
CREATE FUNCTION wallet_balance_matches_last_entry() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    subject uuid;
    stored  bigint;
    last    bigint;
BEGIN
    IF TG_TABLE_NAME = 'wallets' THEN
        subject := NEW.id;
    ELSE
        subject := NEW.wallet_id;
    END IF;

    SELECT balance_cents INTO stored FROM wallets WHERE id = subject;
    IF stored IS NULL THEN
        RETURN NULL;
    END IF;

    SELECT balance_after_cents INTO last
      FROM ledger_entries
     WHERE wallet_id = subject
     ORDER BY sequence_number DESC, id DESC
     LIMIT 1;

    IF stored <> COALESCE(last, 0) THEN
        RAISE EXCEPTION 'wallet balance does not match the last ledger entry'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER wallets_balance_matches_last_entry
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_balance_matches_last_entry();

CREATE CONSTRAINT TRIGGER ledger_entries_balance_matches_wallet
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_balance_matches_last_entry();

-- O papel da aplicação só tem SELECT e INSERT no ledger. Ele nasce NOLOGIN e o
-- papel que conecta recebe a associação: a aplicação entra com SET ROLE, então
-- o privilégio vale mesmo quando quem conecta é superusuário.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wager_app') THEN
        CREATE ROLE wager_app NOLOGIN;
    END IF;
END;
$$;

GRANT USAGE ON SCHEMA public TO wager_app;
GRANT SELECT, INSERT, UPDATE ON wallets TO wager_app;
GRANT SELECT, INSERT, UPDATE ON wager_transactions TO wager_app;
GRANT SELECT, INSERT ON ledger_entries TO wager_app;
GRANT wager_app TO CURRENT_USER;
