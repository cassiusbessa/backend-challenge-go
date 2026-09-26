# 0003. `ALREADY_REVERSED` decidido por consulta sob o lock, não pelo índice parcial

Status: aceita · 2026-09-26

## Contexto

Cada operação aceita uma única reversão `PROCESSED`. O schema garante isso com um índice único parcial, `wager_transactions_one_processed_reversal_per_reference`. A segunda reversão tem de terminar `REJECTED` com `ALREADY_REVERSED`, sem lançamento. A pergunta é quem decide o token: o índice, como na idempotência ([ADR 0001](0001-indice-unico-como-arbitro-da-idempotencia.md)), ou uma consulta.

## Opções consideradas

1. Mapear o nome do índice para a rejeição em `duplicateOf`, como os dois índices de idempotência.
2. Perguntar ao banco, depois do lock da carteira, se a citada já tem reversão `PROCESSED`, e deixar o índice como invariante.

## Decisão

Opção 2. A diferença para a idempotência não é de estilo — é de **quando** a pergunta é feita.

Os dois índices de idempotência são consultados **antes** do lock, de propósito, e por isso não podem ser o árbitro: duas réplicas passam pela consulta. A pergunta sobre a reversão é feita **depois** do lock da carteira, e a segunda reversão só chega a ela quando a primeira já commitou e é visível. A consulta decide sozinha.

E a opção 1 não funcionaria: a violação de índice aborta a transação SQL inteira. Gravar a rejeição exigiria uma segunda transação, que pode ela mesma falhar e deixar nada gravado — enquanto `go-db-invariants` exige a perdedora gravada como `REJECTED`.

## Consequências

- O nome do índice é declarado em `errors.go` e **deixado de fora** de `duplicateOf`. A constante existe para a decisão ser visível em vez de ausente.
- Uma violação desse índice é falha de infraestrutura: a transação desfaz, e o reenvio do provedor encontra a consulta sob o lock respondendo o token corretamente.
- `ROLLBACK` de um `REFUND` aponta para o estorno, e esse estorno também só aceita uma reversão.

## Onde está no código

- `internal/platform/postgres/errors.go` — `reversalUniqueIndex`, com o comentário que explica a ausência em `duplicateOf`.
- `internal/platform/postgres/transactions.go` — `HasProcessedReversal`, `selectProcessedReversal`.
- `internal/app/submitwager/submitwager.go` — `reversalOf`, chamado depois de `GetForUpdate`.
- `internal/domain/wager/reversal.go` — `prepareReversal` lê `Reference.AlreadyReversed`.
