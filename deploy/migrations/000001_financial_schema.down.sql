-- Reverter em desenvolvimento é este down, ou `docker compose down -v` para
-- recriar do zero. Não há dado em produção a migrar.

DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;

DROP FUNCTION IF EXISTS wallet_balance_matches_last_entry();
DROP FUNCTION IF EXISTS ledger_entries_refuse_change();

-- Os privilégios das três tabelas saem junto com elas. O que sobra é a concessão
-- nominal de USAGE no schema, que é por banco e é desta migration. Ela não é o
-- que dá acesso ao schema: `PUBLIC` tem USAGE em `public` por default do cluster,
-- e todo papel o herda, então este REVOKE desfaz o que a subida concedeu sem
-- fechar a porta que o cluster deixa aberta.
REVOKE USAGE ON SCHEMA public FROM wager_app;

-- `wager_app` não é derrubado, e a associação concedida a quem conecta também
-- não. Papel é objeto de cluster, compartilhado por todos os bancos: o cluster
-- local tem o banco da aplicação e o da suíte, e derrubá-lo ao reverter um deles
-- falha com `role "wager_app" cannot be dropped because some objects depend on
-- it` enquanto o outro existir, deixando a versão de schema suja.
--
-- A subida é simétrica disto: ela guarda o `CREATE ROLE` com checagem de
-- existência, ou seja, já assume que o papel pode preexistir. O inverso de um
-- `up` que só cria o papel quando ele falta é um `down` que o deixa de pé. O
-- que a migration reverte é o que a migration criou naquele banco.
--
-- O papel fica órfão depois do último `down` do cluster: NOLOGIN, e sem nenhum
-- privilégio que esta migration tenha concedido. Ele mantém o USAGE herdado de
-- `PUBLIC`, pela razão acima — `has_schema_privilege` responde `t` para um papel
-- recém-criado e segue respondendo `t` depois deste REVOKE. A subida seguinte o
-- reutiliza, e `docker compose down -v` o leva junto com o cluster.
