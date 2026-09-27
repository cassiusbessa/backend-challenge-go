# 0037. A série e o alerta da reconciliação que não produziu veredito

Status: aceita · 2026-09-26

## Contexto

A reconciliação falha, em vez de responder, quando a leitura falha ou quando a soma do ledger ou a diferença para o saldo não cabe no que `Money` representa ([0031](0031-diferenca-fora-do-int64-falha-a-reconciliacao.md)). Uma carteira nesse estado nunca aparece como divergente: o alerta de divergência é cego a ela, e o observador só deixava uma linha de log a cada passagem. O 0031 aceitou esse custo e deixou para o degrau de operação a série e o alerta que o fechariam.

## Opções consideradas

1. Deixar o estado só no log, como o 0031 aceitou.
2. Contar a falha dentro do método que o observador já chama para registrar falhas, quando ele vier com uma carteira.
3. Uma série por origem, movida pela rota e pelo observador, e um alerta sobre as duas origens.
4. Uma série por origem, movida pela rota e pelo observador por métodos próprios, e um alerta só sobre a origem do observador.

## Decisão

Opção 4. O log sozinho depende de alguém procurar a carteira. Contar dentro do registro de falha genérico faria a contagem depender de um argumento opcional: a página de carteiras que falha, que não tentou veredito nenhum, contaria por um chamador que passasse uma carteira por engano. Por isso o observador ganha `Unverified`, separado de `Failed`, e a rota ganha `Unreconciled`, que só conta a falha de infraestrutura e o defeito — a carteira inexistente e o identificador inválido são recusa, não veredito que falhou.

O alerta olha só o observador porque a falha da rota já foi respondida a quem chamou, com 500 ou 503, e a latência por código já a mostra; o que ninguém vê é a carteira que o observador não consegue conferir sem que ninguém peça. Sem `for` e com limiar zero, a mesma forma da divergência: um veredito que falha com a página respondendo já é achado.

## Consequências

- `wager_reconciliation_failures_total{origin}` existe a zero nas duas origens desde a subida do processo, para a primeira falha ser lida como aumento.
- `ReconciliationVerdictFailed` dispara quando a origem `watch` subiu nos últimos 15 minutos, com o teste do `promtool` cobrindo a falha do observador, a falha só da rota e a série estável; o painel mostra a falha no topo, ao lado da divergência, e por origem na faixa de reconciliação.
- A falha do veredito continua avançando o cursor como o [0026](0026-veredito-que-falha-avanca-o-cursor-do-observador.md) fixa, e o turno cortado pelo encerramento não conta.
- **O que se paga:** uma falha passageira do banco exatamente entre a página e o veredito acende o alerta por 15 minutos; a linha de log com o `walletId` diz qual carteira foi. Não há exercício de falha que produza o estado de propósito: ele só existe com escrita que contorna as constraints, e a prova é o teste das regras e o de unidade.

## Onde está no código

- `internal/platform/metrics/metrics.go` — `ReconciliationFailures`, `ReconciliationFailed` e `primeReconciliation`.
- `internal/platform/divergencewatch/reporter.go` — `Unverified`, separado de `Failed`.
- `internal/platform/walletapi/reporter.go` — `Unreconciled`.
- `deploy/prometheus/rules/settlement.yml` — `ReconciliationVerdictFailed`, e o teste em `settlement_test.yml`.
- `deploy/grafana/dashboards/liquidacao.json` — o `stat` do topo e o painel por origem.
