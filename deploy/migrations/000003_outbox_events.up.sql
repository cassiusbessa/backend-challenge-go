-- A outbox. A linha do evento entra no mesmo commit do saldo, do lançamento e
-- da transação, e o relay a move para fora depois. Nenhuma das três tabelas
-- financeiras é tocada por esta migration.
--
-- Todo comando é reaplicável: a migration roda como Job antes das réplicas, e
-- uma segunda aplicação contra um banco já migrado termina sem erro.

CREATE TABLE IF NOT EXISTS outbox_events (
    -- O identificador do evento é a chave primária. É ele que o envelope leva,
    -- que o broker usa como deduplicação e que a republicação repete, então uma
    -- segunda coluna de identidade só daria duas respostas para a mesma
    -- pergunta.
    event_id        uuid        PRIMARY KEY,
    event_type      text        NOT NULL,

    -- A carteira ordena a publicação e é o grupo da mensagem no tópico FIFO.
    wallet_id       uuid        NOT NULL REFERENCES wallets (id),
    payload         jsonb       NOT NULL,
    correlation_id  text,

    -- O trace do commit que gravou a linha. O relay abre span próprio e o liga
    -- a este, inclusive depois de um restart, quando o span original já não
    -- existe no processo.
    trace_id        text,
    span_id         text,

    created_at      timestamptz NOT NULL,
    next_attempt_at timestamptz NOT NULL,
    attempt_count   integer     NOT NULL DEFAULT 0,

    -- A reivindicação: token novo a cada turno, e o prazo medido pelo relógio
    -- do banco. A confirmação só vale se o token ainda for o da reivindicação.
    lease_token     uuid,
    lease_until     timestamptz,

    published_at    timestamptz,
    dead_at         timestamptz,

    CONSTRAINT outbox_events_attempt_count_is_not_negative CHECK (attempt_count >= 0),

    CONSTRAINT outbox_events_published_and_dead_do_not_coexist CHECK (
        published_at IS NULL OR dead_at IS NULL
    ),

    -- O token e o prazo do lease são um par: uma linha com um e sem o outro é
    -- uma reivindicação que ninguém pode confirmar nem expirar.
    CONSTRAINT outbox_events_lease_is_whole CHECK (
        (lease_token IS NULL) = (lease_until IS NULL)
    )
);

-- O índice da varredura. A consulta percorre, por carteira, a linha ainda não
-- publicada e não descartada mais antiga, e é a única que ordena assim.
--
-- Parcial porque a fila é a fração mínima da tabela enquanto o relay acompanha:
-- a linha publicada sai do índice e deixa de mantê-lo.
CREATE INDEX IF NOT EXISTS outbox_events_publish_queue
    ON outbox_events (wallet_id, created_at, event_id)
    WHERE published_at IS NULL AND dead_at IS NULL;

-- O payload é imutável depois da inserção, e com ele a identidade do evento: a
-- republicação repete os dois, e nenhum caminho os reescreve, nem para corrigir
-- nem para completar. O que o relay escreve é o estado da entrega — lease,
-- tentativa, publicação e morte —, e é só isso que o gatilho deixa passar.
--
-- O gatilho recusa também o superusuário, que passa por cima de qualquer GRANT.
CREATE OR REPLACE FUNCTION outbox_events_refuse_payload_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.event_id   IS DISTINCT FROM OLD.event_id
    OR NEW.event_type IS DISTINCT FROM OLD.event_type
    OR NEW.wallet_id  IS DISTINCT FROM OLD.wallet_id
    OR NEW.payload    IS DISTINCT FROM OLD.payload
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'outbox_events payload is immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS outbox_events_refuse_payload_update ON outbox_events;

CREATE TRIGGER outbox_events_refuse_payload_update
    BEFORE UPDATE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_events_refuse_payload_change();

-- A aplicação insere a linha no commit e atualiza o estado da entrega. Ela não
-- apaga: a linha morta permanece, com o mesmo eventId e o mesmo payload.
GRANT SELECT, INSERT, UPDATE ON outbox_events TO wager_app;
