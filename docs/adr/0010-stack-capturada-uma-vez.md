# 0010. Stack capturada uma vez, só em falha de infraestrutura

Status: aceita · 2026-09-26

## Contexto

Uma falha de infraestrutura — banco fora, prazo estourado, broker recusando — precisa de stack para ser investigada. Uma rejeição de negócio não: saldo insuficiente é resultado esperado. E uma stack capturada em cada fronteira que o erro atravessa produz cinco cópias da mesma trilha.

## Opções consideradas

1. `pkg/errors` ou similar, com stack em todo `Wrap`.
2. Sem stack: confiar no trace.
3. `internal/platform/fault`: captura `runtime.Callers` na fronteira onde a falha é vista **primeiro**, e o embrulho seguinte só acrescenta a operação.

## Decisão

Opção 3. `fault.Wrap` verifica se a cadeia já carrega um `*fault.Error` e, se carrega, não captura de novo. Uma falha, uma stack.

A opção 1 produz uma stack por fronteira e as empilha na mensagem. A 2 perde o que o trace não tem: o trace mostra os spans, não a linha de Go onde o `pgx` devolveu erro.

A rejeição de negócio não passa por `fault`. É isso que separa as duas classes de `go-errors` no nível do valor: ser um `*fault.Error` na cadeia é o marcador de infraestrutura que a borda lê.

## Consequências

- Quem decide o desfecho é quem loga, e loga uma vez: a borda HTTP, o consumidor da fila ou o worker chamam `span.RecordError(err, trace.WithStackTrace(true))` e logam `fault.Stack(err)` no atributo `stack`. Nem o caso de uso, nem o domínio, nem o adaptador SQL logam.
- Um defeito que nunca cruzou I/O não tem `fault` na cadeia. A borda captura ali, porque ali é onde ele é visto como falha pela primeira vez.
- `problem.carriesStack` é o que distingue uma indisponibilidade real de um erro que ninguém classificou — e este último sai como `500`, não `503`, para não convidar o provedor a repetir o que nunca vai passar.

## Onde está no código

- `internal/platform/fault/fault.go` — `Wrap`, `capture`, `Stack`.
- `internal/platform/postgres/errors.go` — `wrap`: a fronteira que decide a classe.
- `internal/platform/wagerapi/reporter.go` — `record`, `stackOf`.
- `internal/platform/problem/problem.go` — `carriesStack`, `markedClassOf`.
