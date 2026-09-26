# 0005. Moeda gravada na transação e no lançamento, embora derivável da carteira

Status: aceita · 2026-09-26

## Contexto

Uma carteira tem exatamente uma moeda, e `wager_transactions.wallet_id` é chave estrangeira para ela. Pela terceira forma normal, `wager_transactions.currency` e `ledger_entries.currency` são redundantes. Um `LOSS`, que nunca move saldo, ainda assim carrega moeda — e a pergunta natural é por quê.

## Opções consideradas

1. Derivar a moeda de `wallets` por `JOIN`, sem coluna própria nas tabelas históricas.
2. Validar a moeda do corpo contra a carteira e não gravá-la.
3. Gravar a moeda no lançamento e na transação, e prender as duas com a chave estrangeira composta.

## Decisão

Opção 3. A raiz não é a transação — é o lançamento.

`ledger_entries` é imutável por gatilho; `wallets` é atualizada em todo movimento, e nada no schema proíbe um `UPDATE` em `wallets.currency`. Um registro histórico que derivasse a moeda por `JOIN` seria **reinterpretável**: uma linha alterada, e anos de lançamentos passariam a ser lidos em outra moeda. O lançamento afirma os próprios fatos.

Dado que o lançamento carrega moeda, ele pode divergir da transação. A chave estrangeira composta `(transaction_id, wallet_id, currency, amount_cents)` é o que prova que os dois concordam — e para isso a coluna precisa existir em `wager_transactions`, `NOT NULL`, em toda linha.

## Consequências

- `LOSS` carrega moeda porque a coluna é da tabela, não do kind. Ele paga por uma garantia que não usa.
- A moeda **no corpo** tem valor próprio, e é por ser derivável: se os dois campos têm de concordar, a discordância acusa um `walletId` errado. Um jogador pode ter uma carteira por moeda, e `guard` rejeita com `CURRENCY_MISMATCH` um `LOSS` de `0.00 BRL` contra a carteira em EUR.
- Toda leitura de transação e de extrato é consulta de tabela única.
- O evento `WagerTransactionProcessed` de um `LOSS` sai com `{"amount":"0.00","currency":"BRL"}`, um `Money` válido no contrato de saída.

## Onde está no código

- `deploy/migrations/000001_financial_schema.up.sql` — `wager_transactions_movement_key`, `ledger_entries_match_their_transaction`, o gatilho do ledger e o `GRANT UPDATE` em `wallets`.
- `internal/domain/wager/action.go` — `guard` compara a moeda da operação com a da carteira.
- `internal/domain/wager/transaction.go` — `checkAmountForKind` exige moeda para todo kind.
