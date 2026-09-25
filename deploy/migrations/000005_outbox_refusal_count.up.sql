-- A contagem que mata a linha. `go-outbox` diz "rejeição permanente do broker,
-- dez vezes, marca a linha como morta", e `attempt_count` não responde isso:
-- ele sobe em toda tentativa que não publicou, inclusive a transitória. Um
-- broker fora do ar por dois minutos deixa nove tentativas atrás de si, e a
-- primeira recusa permanente depois disso seria a décima.
--
-- Todo comando é reaplicável: a migration roda como Job antes das réplicas, e
-- uma segunda aplicação contra um banco já migrado termina sem erro.

-- `attempt_count` continua contando toda tentativa e movendo o backoff. Quem
-- decide a morte é esta coluna.
ALTER TABLE outbox_events
    ADD COLUMN IF NOT EXISTS refusal_count integer NOT NULL DEFAULT 0;

ALTER TABLE outbox_events
    DROP CONSTRAINT IF EXISTS outbox_events_refusal_count_is_not_negative;

ALTER TABLE outbox_events
    ADD CONSTRAINT outbox_events_refusal_count_is_not_negative CHECK (refusal_count >= 0);

