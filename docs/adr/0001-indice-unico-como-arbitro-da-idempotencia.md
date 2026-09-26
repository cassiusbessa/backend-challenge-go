# 0001. Índice único como árbitro da idempotência

Status: aceita · 2026-09-26

## Contexto

O mesmo request pode chegar duas vezes — retry do provedor, duplicação de um balanceador, reentrega da fila — e em réplicas diferentes ao mesmo tempo. A segunda chegada tem de responder o resultado gravado sem apostar de novo. Duas chaves identificam uma chegada: `(providerId, idempotencyKey)` e `(providerId, externalTransactionId)`.

## Opções consideradas

1. Consultar antes de gravar: `SELECT` pela chave e, se existir, responder o replay.
2. Isolamento `SERIALIZABLE` na unit of work, e deixar o banco abortar a segunda.
3. Índices únicos sobre as duas chaves, e tratar a violação como a decisão.

## Decisão

Opção 3. Os dois índices únicos são o árbitro; a consulta prévia existe, mas só como caminho rápido do caso comum.

A opção 1 tem uma janela: duas réplicas leem "não existe" antes de qualquer uma gravar, e as duas gravam. Nenhum nível de isolamento barato fecha isso. A opção 2 troca a janela por abortos de serialização e retries sob contenção na mesma carteira — que é exatamente o caso que a fila FIFO por carteira torna comum.

O índice é atômico por construção: quando duas réplicas inserem a mesma chave, uma ganha e a outra recebe `23505` com o nome do índice violado. Não há janela.

## Consequências

- `submitwager.recorded` lê pela chave **sem lock** e antes de travar a carteira. Não é correção, é otimização: o provedor reenviando cinco minutos depois responde o replay sem lock, sem movimento e sem `INSERT` fadado a falhar. Duas réplicas podem ambas não achar nada, e está certo.
- A perdedora precisa reler a linha vencedora **fora** da transação que a violação abortou: `afterRace` usa `storage.Reads.TransactionByKey`, do pool, depois do rollback.
- Uma chegada gêmea viola os dois índices, e o PostgreSQL nomeia só o primeiro que checou. O nome do índice não diz sozinho qual chegada é esta; quem diz é a linha vencedora — mesmo hash é replay, hash diferente é `IDEMPOTENCY_CONFLICT`, nenhuma linha sob a chave é `DUPLICATE_EXTERNAL_TRANSACTION`.
- O movimento que a perdedora calculou morre com o rollback. É o único efeito colateral que a especificação pede.

## Onde está no código

- `deploy/migrations/000001_financial_schema.up.sql` — `wager_transactions_one_per_provider_and_key`, `wager_transactions_one_per_provider_and_external_id`.
- `internal/platform/postgres/errors.go` — `refusalOf`, `duplicateOf`: o nome do índice vira o token.
- `internal/app/submitwager/submitwager.go` — `recorded`, `afterRace`, `raced`, `vanished`.
