-- As duas colunas saem, e o índice volta à ordem anterior. A tabela e as linhas
-- permanecem: o que esta reversão desfaz é a ordem da varredura e a contagem
-- das recusas, não a outbox.
--
-- Desfeita, a publicação volta a ser ordenada por `created_at` — com a inversão
-- que a subida descreve — e a morte da linha volta a contar tentativas.

DROP INDEX IF EXISTS outbox_events_publish_queue;

CREATE INDEX IF NOT EXISTS outbox_events_publish_queue
    ON outbox_events (wallet_id, created_at, event_id)
    WHERE published_at IS NULL AND dead_at IS NULL;

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

ALTER TABLE outbox_events
    DROP COLUMN IF EXISTS publish_seq;
