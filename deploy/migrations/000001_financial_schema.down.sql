-- Reverter em desenvolvimento é este down, ou `docker compose down -v` para
-- recriar do zero. Não há dado em produção a migrar.

DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;

DROP FUNCTION IF EXISTS wallet_balance_matches_last_entry();
DROP FUNCTION IF EXISTS ledger_entries_refuse_change();

REVOKE USAGE ON SCHEMA public FROM wager_app;
DROP ROLE IF EXISTS wager_app;
