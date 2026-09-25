-- O índice e a constraint saem. Nenhuma tabela, privilégio ou linha é tocada por
-- esta migration, então desfazê-la não tem mais nada a devolver.

ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_transactions_waiting_has_deadline;

DROP INDEX IF EXISTS wager_transactions_wait_queue;
