-- A coluna sai. A tabela e as linhas permanecem: desfeita, a morte da linha
-- volta a contar tentativas em vez de recusas permanentes.

ALTER TABLE outbox_events
    DROP CONSTRAINT IF EXISTS outbox_events_refusal_count_is_not_negative;

ALTER TABLE outbox_events
    DROP COLUMN IF EXISTS refusal_count;
