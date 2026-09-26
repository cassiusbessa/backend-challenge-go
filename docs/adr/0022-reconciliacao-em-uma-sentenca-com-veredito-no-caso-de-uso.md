# 0022. Reconciliação em uma sentença, sem lock, com o veredito no caso de uso

Status: aceita · 2026-09-26

## Contexto

O banco prova em gatilho diferido que, em todo commit, o saldo da carteira é o `balance_after` do último lançamento, e o ledger recusa `UPDATE` e `DELETE` até de superusuário. O que o schema **não** prova: que o saldo anterior de cada lançamento é o posterior do anterior, e que a sequência não tem furo. A reconciliação existe para o que sobra — e para o dia em que uma escrita contorna a aplicação e o gatilho. A regra de leituras exige que o saldo armazenado e o calculado saiam de um mesmo snapshot: duas sentenças em `READ COMMITTED` veriam um commit no meio e inventariam um desvio que não existe.

## Opções consideradas

1. Uma única `SELECT` que junta a linha da carteira ao ledger agregado daquela carteira, e o caso de uso decide o veredito sobre os números.
2. Uma transação em `REPEATABLE READ` com duas sentenças — a carteira e o agregado — e o veredito no caso de uso.
3. Apoiar-se no gatilho diferido e devolver só o saldo armazenado: se commitou, fecha.
4. A sentença única com `CASE WHEN` decidindo `consistent` e os tokens dentro do SQL.

## Decisão

Opção 1. Uma sentença é um snapshot em `READ COMMITTED`, que é o que a regra aceita, e uma `SELECT` sem `FOR UPDATE` não espera uma aposta no meio do commit — a suíte de jornada afirma a resposta com a carteira travada por outra transação.

A opção 2 compra o mesmo snapshot pagando uma transação para administrar e uma ida a mais ao banco; ela não tem o que dar errado a mais, só o que fazer a mais. A opção 3 confere um lançamento contra o saldo, e só no commit: não vê furo de sequência nem quebra no meio da corrente, e não roda numa leitura — é exatamente o caso que a rota existe para achar. A opção 4 é a mesma sentença com a regra dentro: o veredito só seria exercitado pela suíte tagueada, o gremlins nunca o veria, e o observador periódico do próximo change teria de repetir o SQL para obter o mesmo resultado. Com os números no Go, `reconcilewallet` é a única fonte do veredito, testada sem banco, e a rota é só o primeiro chamador.

A quebra de corrente fica no SQL, medida por `lag` sobre `(sequence_number, id)`, e sai como a **menor** sequência em que o saldo anterior difere do que o lançamento anterior deixou: é *onde*, não só *se*, e precisa do ledger inteiro — carregar milhares de linhas para calcular um número não é uma leitura. O `SUM` de `bigint` é `numeric` e volta a `bigint` na sentença: um ledger cuja soma não cabe em `int64` falha a conversão e sai como falha de infraestrutura, com stack, porque é um estado fora do que `Money` representa e não há veredito honesto a dar.

## Consequências

- Os dois saldos, a contagem, a última sequência e a primeira quebra vêm do mesmo instante, sem transação e sem lock; a leitura nunca fica atrás de uma aposta.
- O vocabulário de divergência — `BALANCE_MISMATCH`, `SEQUENCE_GAP`, `CHAIN_BREAK` — mora no caso de uso e não é `failureCode`: nomeia um desvio numa leitura, não uma regra que recusou uma operação.
- A divergência loga na borda e não marca o span: é resultado, não falha. O observador do próximo change chama o mesmo `Service` e loga por conta própria.
- **O que se paga:** a sentença é O(lançamentos da carteira) e o cliente reconcilia uma carteira por vez; quem varrer todas decide a amostragem. E o saldo calculado pode ser negativo num ledger quebrado — `money.FromCents` aceita isso de propósito, e o relatório diz o que o ledger soma em vez de recusar dizer.

## Onde está no código

- `internal/platform/postgres/ledger.go` — `selectLedgerSummary`, a sentença com as CTEs, e `Reads.Summary`.
- `internal/app/storage/storage.go` — `LedgerSummary`, os agregados crus que a porta devolve.
- `internal/app/reconcilewallet/reconcilewallet.go` — `divergencesOf`, o veredito; `Divergence`, o vocabulário.
- `internal/platform/walletapi/reporter.go` — `Reporter.Diverged`, a linha de log sem saldo.
- `internal/e2e/wallet/reconciliation_test.go` — `TestReconcile_doesNotWaitForALockedWallet`, `TestReconcile_namesABalanceWrittenPastTheLedger`.
