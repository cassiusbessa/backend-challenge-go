-- O índice da fila de espera. O worker de referência varre as linhas cuja
-- próxima tentativa já chegou, e esta é a única consulta que ordena por esse
-- instante.
--
-- Parcial porque a fila é uma fração mínima da tabela: as linhas terminais, que
-- são a esmagadora maioria, não entram no índice nem o mantêm.
CREATE INDEX IF NOT EXISTS wager_transactions_wait_queue
    ON wager_transactions (next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';

-- O prazo da espera passa a ser carregado por este change, e nada no schema
-- exigia que uma linha em espera o tivesse. Um PENDING_REFERENCE sem prazo é uma
-- linha que expira na primeira tentativa: o worker lê o zero de time.Time,
-- conclui que o prazo já passou e fecha a espera em REJECTED um tick depois da
-- entrada, em vez de quinze minutos depois. O par desta constraint é
-- wager_transactions_waiting_has_next_attempt, que já cobre a agenda.
-- O DROP antes do ADD é o que torna o par reaplicável, porque PostgreSQL não tem
-- ADD CONSTRAINT IF NOT EXISTS. As duas ações vão no mesmo ALTER, então a tabela
-- nunca fica sem a constraint: ou o comando inteiro passa, ou nada muda.
ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS wager_transactions_waiting_has_deadline,
    ADD CONSTRAINT wager_transactions_waiting_has_deadline CHECK (
        status <> 'PENDING_REFERENCE' OR reference_deadline_at IS NOT NULL
    );
