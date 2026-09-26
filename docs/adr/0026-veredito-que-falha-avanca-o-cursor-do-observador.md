# 0026. Um veredito que falha avança o cursor do observador

Status: aceita · 2026-09-26

Substitui parcialmente [0024](0024-observador-de-divergencia-por-cursor-em-memoria.md): a consequência "uma falha encerra o turno sem mover o cursor". O resto do 0024 continua valendo.

## Contexto

O [ADR 0024](0024-observador-de-divergencia-por-cursor-em-memoria.md) fez toda falha do turno — da página ou de um veredito — encerrar o turno com o cursor parado, para nenhuma carteira ser pulada e um banco que caiu ser retomado sem reinício. As duas falham por razões diferentes. A página é uma consulta sobre `wallets` que não depende de carteira nenhuma; o veredito lê o ledger de uma carteira e pode falhar sempre para a mesma: a soma estourando o `BIGINT` com lançamentos inseridos por fora, uma linha que a reidratação recusa, ou o `statement_timeout` ainda não fixado ([06 · Riscos](../06-riscos-e-limitacoes.md)) atingindo sempre a carteira de ledger maior. Com o cursor parado, essa carteira trava a varredura: as que vêm depois nunca mais são conferidas, e `wager_reconciliation_wallets_checked_total{origin="watch"}` continua subindo sempre que ela não é a primeira da página, porque as anteriores são reconferidas a cada tick. O contador que o 0024 pôs ao lado da divergência para dizer que o observador parou não diz.

## Opções consideradas

1. Manter o cursor parado em qualquer falha, como no 0024.
2. Registrar a falha do veredito e seguir a página até o fim.
3. Tentar a mesma carteira até N vezes, e só então passar adiante.
4. Encerrar o turno na falha do veredito com o cursor na carteira que falhou; a falha da página continua sem mover o cursor.

## Decisão

Opção 4. A opção 1 troca uma carteira que não fecha por todas as que vêm depois dela, e esconde isso do contador de vida. A opção 2 também destrava, mas numa queda do banco no meio da página — a página já lida, os vereditos falhando — escreve uma linha de erro por carteira do lote a cada tick, cinquenta pelo padrão, onde uma basta. A opção 3 guarda em memória uma contagem de tentativas por carteira e um N escolhido sem medida, para chegar ao mesmo lugar que a 4 com N turnos de atraso.

A queda do banco que o 0024 queria atravessar sem pular carteira continua contida, só que pela página: ela lê o mesmo banco, falha antes de qualquer veredito, e não move o cursor. O que a opção 4 pula é a carteira cujo veredito falhou com a página respondendo.

## Consequências

- Uma carteira cujo veredito falha sempre é lida uma vez por passagem, e deixa a cada vez uma linha de falha com o `walletId` e a stack. As carteiras depois dela continuam sendo conferidas.
- `wallets_checked_total{watch}` volta a querer dizer que a varredura anda.
- **O que se paga:** uma falha passageira do veredito, com a página respondendo, adia aquela carteira para a passagem seguinte — o número de carteiras dividido pelo lote, vezes o intervalo. A falha do veredito não tem série própria; aparece só no log.

## Onde está no código

- `internal/platform/divergencewatch/divergencewatch.go` — `Worker.turn`: o cursor na carteira cujo veredito falhou, e parado quando é a página que falha.
- `internal/platform/divergencewatch/divergencewatch_test.go` — `TestTurn_movesPastTheWalletWhoseVerdictFails`, `TestTurn_checksNothingWhenThePageFails`.
