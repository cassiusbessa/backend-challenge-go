# 0002. Lock pessimista da carteira com guarda de versão em `READ COMMITTED`

Status: aceita · 2026-09-26

## Contexto

Duas apostas contra a mesma carteira, ao mesmo tempo, em réplicas diferentes, não podem produzir saldo negativo nem perder um débito. O caso obrigatório do desafio: duas apostas de 80 contra saldo 100 terminam em uma processada, uma rejeitada, saldo 20 e um lançamento. Há várias réplicas, então nada em memória serve.

## Opções consideradas

1. Mutex no processo.
2. Controle otimista puro: ler, calcular, `UPDATE … WHERE version = $lida`, e tentar de novo quando zero linhas.
3. `SERIALIZABLE`, deixando o banco abortar o conflito.
4. `SELECT … FOR UPDATE` na linha da carteira antes de decidir, mais o `UPDATE` condicionado à versão lida, em `READ COMMITTED`.

## Decisão

Opção 4. O lock serializa a decisão: a segunda aposta espera, lê o saldo já commitado e, se não couber, rejeita com `INSUFFICIENT_FUNDS` — que é o resultado certo, não um retry.

A opção 1 não sobrevive a réplicas. A 2 faz a segunda aposta calcular sobre saldo velho e descobrir só na escrita, e o retry de negócio sob contenção é caro e visível ao provedor. A 3 tem custo alto sob a contenção que a fila por carteira concentra.

A versão fica no `UPDATE` mesmo com o lock: é a assertiva de que nada passou por ele. Zero linhas afetadas é escrita que furou o lock — não deveria acontecer nunca, e é por isso que existe.

## Consequências

- A ordem é **carteira, depois transação**, em todo caminho: submissão, worker de referência, encerramento da espera. Inverter em um deles é deadlock com os outros.
- `ErrLostWrite` é falha transitória, sem `failureCode`: a transação SQL desfaz e a operação é tentada de novo. A borda a classifica como indisponibilidade, com stack, e não deixa a classe depender de qual fronteira a embrulhou.
- A versão sobe exatamente um quando o saldo muda, e só então. A sequência do lançamento é a versão resultante — não há segundo contador.
- Carteiras diferentes não compartilham lock. Não há lock de tabela.

## Onde está no código

- `internal/platform/postgres/wallets.go` — `lockWallet`, `updateWalletBalance`, `UpdateBalance`.
- `internal/app/storage/storage.go` — `ErrLostWrite`, `Wallets.GetForUpdate`.
- `internal/app/submitwager/submitwager.go` — `apply`: a ordem lock → citada → decisão → escrita.
- `internal/platform/problem/problem.go` — `namedClassOf` nomeia `ErrLostWrite` como `Unavailable`.
