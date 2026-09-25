-- A inbox. A linha entra no mesmo commit do saldo, do lançamento, da transação
-- e das linhas de outbox, e é ela que deduz a reentrega da mensagem. Nenhuma das
-- tabelas financeiras é tocada por esta migration.
--
-- Todo comando é reaplicável: a migration roda como Job antes das réplicas, e
-- uma segunda aplicação contra um banco já migrado termina sem erro.

CREATE TABLE IF NOT EXISTS inbox_messages (
    -- A unicidade é o par, e não o identificador sozinho: dois consumidores do
    -- mesmo envelope registram cada um a sua linha, e nenhum esconde o outro.
    consumer   text        NOT NULL,
    message_id text        NOT NULL,

    -- O hash do corpo recebido. É ele que distingue a reentrega legítima do
    -- mesmo identificador reapresentado com outro conteúdo, e é por isso que ele
    -- fica na linha em vez de ser recalculado a cada entrega.
    body_hash  text        NOT NULL,

    created_at timestamptz NOT NULL,

    CONSTRAINT inbox_messages_one_per_consumer_and_message
        PRIMARY KEY (consumer, message_id),

    CONSTRAINT inbox_messages_consumer_is_not_empty CHECK (consumer <> ''),
    CONSTRAINT inbox_messages_message_id_is_not_empty CHECK (message_id <> ''),
    CONSTRAINT inbox_messages_body_hash_is_not_empty CHECK (body_hash <> '')
);

-- A linha é imutável depois da inserção. Quem perde a unicidade relê a linha
-- gravada e compara o hash, então um hash reescrito transformaria uma reentrega
-- de outro corpo em reentrega legítima — e a decisão sobre a reentrega é
-- justamente essa comparação.
--
-- O gatilho recusa também o superusuário, que passa por cima de qualquer GRANT.
CREATE OR REPLACE FUNCTION inbox_messages_refuse_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'inbox_messages accepts INSERT only'
        USING ERRCODE = 'restrict_violation';
END;
$$;

DROP TRIGGER IF EXISTS inbox_messages_refuse_update_and_delete ON inbox_messages;

CREATE TRIGGER inbox_messages_refuse_update_and_delete
    BEFORE UPDATE OR DELETE ON inbox_messages
    FOR EACH ROW EXECUTE FUNCTION inbox_messages_refuse_change();

DROP TRIGGER IF EXISTS inbox_messages_refuse_truncate ON inbox_messages;

CREATE TRIGGER inbox_messages_refuse_truncate
    BEFORE TRUNCATE ON inbox_messages
    FOR EACH STATEMENT EXECUTE FUNCTION inbox_messages_refuse_change();

-- A aplicação insere a linha no commit e a relê quando a unicidade recusa. Ela
-- não atualiza e não apaga: o papel não tem esses privilégios, e o gatilho acima
-- recusa quem os tiver.
GRANT SELECT, INSERT ON inbox_messages TO wager_app;
