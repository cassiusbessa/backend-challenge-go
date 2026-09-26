# 0031. A diferença que não cabe em `int64` falha a reconciliação

Status: aceita · 2026-09-26

## Contexto

O [0029](0029-contrato-http-segue-o-enunciado.md) pôs a `difference` — o saldo gravado menos o calculado — no relatório que `reconcilewallet` decide junto com o veredito, e que a rota e o observador de divergência dividem. Os dois saldos cabem em `int64` e a diferença pode não caber: o gravado vai até o teto, e o calculado fica negativo quando a corrente do ledger se quebra, o que o schema não impede para uma escrita que contorna a aplicação ([0022](0022-reconciliacao-em-uma-sentenca-com-veredito-no-caso-de-uso.md)). Passar do limite pede uma distância de cerca de 92 quatrilhões de reais entre os dois. Ao contrário da soma que não cabe, que o 0022 já trata como falha porque não deixa comparar nada, aqui o veredito é certo — a carteira diverge —, e falta só o número. Antes do 0029, essa carteira saía `BALANCE_MISMATCH` e entrava na série que o alerta de divergência observa.

## Opções consideradas

1. Falhar a reconciliação quando a diferença não cabe, como já falha quando a soma não cabe.
2. Manter o veredito e deixar a diferença fora do relatório quando ela não cabe, para o observador contar a divergência.
3. Dar ao veredito que falha uma série própria e um alerta, para toda carteira cuja reconciliação falha sempre aparecer, qualquer que seja o motivo.

## Decisão

Opção 1. A opção 2 cobre metade do ponto cego: a soma que não cabe continua falhando sem veredito, porque ali não há comparação honesta a fazer, e o alerta passaria a ver um dos dois estados fora do que `Money` representa e não o outro. Ela ainda abre no contrato um caso em que falta a `difference` que o enunciado sempre mostra, para um estado que só existe com quatrilhões escritos à mão no banco. A opção 3 é a que fecha o ponto cego nos dois estados e em qualquer carteira que falhe sempre; mas acrescenta uma série e um terceiro alerta às duas regras que o [0025](0025-alertas-como-regras-do-prometheus-testadas.md) testa, e cabe no degrau de operação, junto do guia de falha, não num change de contrato.

## Consequências

- Uma reconciliação que responde 200 traz sempre a `difference`: o relatório tem todos os números ou não existe.
- A carteira cuja diferença não cabe responde 500 na rota; o observador registra o veredito que falha, com o `walletId` e a stack, a cada passagem, e segue para as seguintes ([0026](0026-veredito-que-falha-avanca-o-cursor-do-observador.md)).
- **O que se paga:** o alerta de divergência não vê essa carteira, como já não vê a de soma que não cabe; o sinal é a linha de falha de cada passagem, que nenhum alerta observa. É uma regressão em relação a antes do 0029, restrita a um estado que a aplicação não produz. E os dois estados saem com números diferentes na borda: a soma, 503, porque chega do driver já como falha de infraestrutura; a diferença, 500, porque repetir não a corrige.

## Onde está no código

- `internal/app/reconcilewallet/reconcilewallet.go` — `reportOf`, que subtrai antes de montar o relatório e falha sem ele.
- `internal/app/reconcilewallet/reconcilewallet_test.go` — `TestReconcile_refusesADifferencePastTheRangeOfMoney`.
- `internal/domain/money/arithmetic.go` — `Money.Sub`, que recusa o overflow em vez de dar a volta.
- `internal/platform/divergencewatch/divergencewatch.go` — `Worker.turn`, que registra o veredito que falha e avança.
