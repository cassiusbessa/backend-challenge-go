# 0024. Observador de divergência como quarto runner, varrendo por cursor em memória

Status: aceita · 2026-09-26 · parcialmente substituída por [0026](0026-veredito-que-falha-avanca-o-cursor-do-observador.md) · 2026-09-26

## Contexto

A reconciliação produz o veredito de uma carteira quando alguém chama a rota ([ADR 0022](0022-reconciliacao-em-uma-sentenca-com-veredito-no-caso-de-uso.md)). Uma carteira cujo saldo se descolou do ledger só aparece no log de quem, por acaso, pediu. A divergência que importa é justamente a que nenhuma escrita da aplicação produz — um saldo gravado por fora, um lançamento inserido sem o gatilho — e ela não deixa rastro em coluna nenhuma que a aplicação atualize. O observador tem de achar essa carteira sem ninguém pedir, sem escrever e sem ficar no caminho de uma aposta.

## Opções consideradas

1. `postgres_exporter` com uma query customizada que o Prometheus raspa.
2. Uma sentença agregada sobre todas as carteiras por turno, devolvendo quantas divergem.
3. Um quarto runner que varre só as carteiras com `updated_at` recente.
4. Um quarto runner que reivindica carteiras por uma coluna `checked_at`, com `SKIP LOCKED`, para réplicas dividirem a varredura.
5. Um quarto runner que varre **todas** as carteiras em páginas, na ordem da identidade, a partir de um cursor em memória, e chama o mesmo `reconcilewallet` que a rota chama.

## Decisão

Opção 5, na forma dos outros runners ([ADR 0011](0011-tres-runners-de-fundo-separados.md)): `Start` devolve na hora, o turno lê uma página, `Stop` fecha o sinal e espera o turno em curso.

A opção 1 põe o veredito em dois lugares, Go e YAML, e a divergência vira um número sem `walletId` na linha de log — que é o que permite agir. A opção 2 devolve só a contagem, custa o ledger inteiro de uma vez, e devolve a regra para dentro do SQL, de onde o ADR 0022 a tirou. A opção 3 deixa de ver exatamente o caso que a reconciliação existe para achar: uma escrita que contornou a aplicação não move `updated_at`. A opção 4 é uma coluna nova e uma escrita numa tabela financeira para um componente cuja razão de ser é não escrever.

A série segue a mesma lógica. `wager_reconciliation_divergences_total` é **contador** por token, e não um gauge de "carteiras divergentes agora": o gauge cairia a zero no turno seguinte, quando a varredura passa adiante, com a carteira ainda quebrada. O alerta pergunta se o contador subiu na janela recente, e isso continua verdadeiro enquanto a carteira continuar divergente, porque cada passagem a reencontra. Ao lado dele, `wager_reconciliation_wallets_checked_total` responde a pergunta que o primeiro não responde: zero divergências porque não há, ou porque o observador parou.

## Consequências

- Toda carteira entra na varredura, inclusive a que não recebeu escrita; o veredito é o da rota, sobre a mesma sentença, e o turno nunca toma lock nem abre transação.
- Uma página curta zera o cursor, e a passagem recomeça. Uma falha encerra o turno sem mover o cursor, então nenhuma carteira é pulada.
- As duas séries de reconciliação nascem em zero para toda origem e todo token: `increase()` sobre uma série que aparece pela primeira vez em 1 responde 0, e a primeira divergência não dispararia o alerta.
- **O que se paga:** o custo de um turno é o lote vezes o ledger de cada carteira, e ainda não foi medido — 50 carteiras a cada 5 s é ponto de partida, ajustável por configuração. Cada réplica varre por conta própria e encontra a mesma divergência, e o alerta soma; são leituras, e ninguém precisa arbitrar entre elas. Uma carteira divergente reaparece no log a cada passagem até alguém corrigir o banco.

## Onde está no código

- `internal/platform/divergencewatch/divergencewatch.go` — `Worker`, `turn`, `advance`: a página, o cursor e a volta ao início.
- `internal/platform/divergencewatch/reporter.go` — `Reporter.Checked`: a linha da divergência e as duas séries com origem `watch`.
- `internal/platform/postgres/reads.go` — `selectWalletIDsAfter`, `Reads.WalletIDsAfter`: a página na ordem da identidade.
- `internal/platform/metrics/metrics.go` — `primeReconciliation`: as séries em zero antes do primeiro veredito.
- `internal/platform/app/app.go` — `newDivergenceWatcher`, `claimants`: o quarto runner e a quarta fatia do prazo.
- `internal/e2e/wallet/metrics_test.go` — `TestWatcher_findsTheBalanceWrittenPastTheLedgerAndCorrectsNothing`.
