# 0004. A rejeição durável viaja ao lado do resultado, não como erro da unit of work

Status: aceita · 2026-09-26

## Contexto

`REJECTED` é resultado registrado: uma aposta recusada por saldo insuficiente deixa uma linha com `failureCode`, consultável para sempre. Ao mesmo tempo, a borda tem de responder essa recusa como `422` com token — um erro, do ponto de vista do chamador.

A unit of work decide commit ou rollback pelo erro que a função de trabalho devolve. Devolver a rejeição como erro faria o rollback apagar a própria linha `REJECTED` que acabou de ser escrita.

## Opções consideradas

1. Devolver a rejeição como erro e aceitar que ela não deixa linha.
2. Um sinal na unit of work — "commite mesmo com erro" — para certos tipos.
3. A função de trabalho devolve `nil`, e a rejeição viaja **num campo** do valor que ela produz; quem a transforma em erro de novo é o chamador, depois do commit.

## Decisão

Opção 3. `settlement{result, rejection}`: `reject` grava a linha, devolve `nil` como erro e carrega a rejeição no campo. `settle` commita. `answerOf` devolve `(result, rejection)` já fora da fronteira transacional.

A opção 1 viola o ciclo de vida. A opção 2 põe uma regra de negócio dentro da unit of work, que deixaria de ser genérica, e cria um erro que às vezes é erro e às vezes não.

## Consequências

- O `errors.As` no começo de `reject` é o portão: só `wager.Rejection` entra por essa porta. Um defeito do ledger, por exemplo, é devolvido como erro de verdade, a transação desfaz e nada é gravado.
- O resultado acompanha a rejeição, porque a linha existe e a borda precisa logar a transação que ela escreveu. Uma recusa sem linha — os dois conflitos de idempotência — carrega o resultado zero.
- Existem duas fontes de `IDEMPOTENCY_CONFLICT`, e só uma passa pelo tratamento de corrida: o conflito decidido por **leitura** viaja no campo e commita; o decidido por **índice** vem como erro e cai em `afterRace`.

## Onde está no código

- `internal/app/submitwager/submitwager.go` — `settlement`, `answerOf`, `settle`, `reject`, `outcomeOf`.
