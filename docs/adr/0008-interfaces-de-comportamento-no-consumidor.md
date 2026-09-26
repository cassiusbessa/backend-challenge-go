# 0008. As interfaces de comportamento do erro são declaradas no consumidor

Status: aceita · 2026-09-26

## Contexto

A borda HTTP precisa saber três coisas sobre um erro que não é rejeição de negócio: se pode ser tentado de novo em instantes (`503` com `Retry-After`), se é defeito nosso (`500`), e se um desfecho gravado está sendo respondido de novo (`idempotentReplay: true`). Quem sabe cada uma dessas coisas é o caso de uso que produziu o erro. A borda não pode importar o caso de uso para perguntar, e o domínio não pode conhecer conceitos da borda.

## Opções consideradas

1. Interfaces exportadas no domínio (`wager.Retryable`), implementadas pelos erros do caso de uso.
2. `problem` importa `submitwager` e compara com `errors.Is` contra as sentinelas.
3. Um campo de código numérico no erro, com tabela na borda.
4. Interfaces **não exportadas**, de um método cada, declaradas em `problem` — `retryable`, `defective`, `replayed` — e satisfeitas estruturalmente pelos tipos do caso de uso, sem que ele saiba disso.

## Decisão

Opção 4. É o idioma do Go: *aceite interfaces, declare-as onde consome*. A biblioteca padrão faz exatamente isso em `errors.Is`, que verifica `interface{ Is(error) bool }` anônima e nunca exportou uma `Iser`.

A opção 1 põe no domínio a ideia de "retry", que é da borda. A 2 acopla a borda a cada caso de uso e cresce um `errors.Is` por sentinela nova. A 3 é uma tabela em dois lugares.

## Consequências

- `problem` não importa nenhum caso de uso. Um caso de uso novo que produza um erro transitório só precisa de um tipo com `RetryShortly() bool`.
- Cada interface é declarada **uma vez**. O que se repete é a implementação: `defect` existe em `submitwager` e em `resolvereference`, dois tipos de três linhas.
- Não há amarração de compilação entre pacotes, de propósito. O que fecha as duas pontas é barato: uma assertiva anônima no produtor, `var _ interface{ RetryShortly() bool } = transient{}`, pega o rename lá; um teste no consumidor que passa os erros reais do caso de uso por `problem.From` pega o rename cá. Sem os dois, um rename degrada `503` em `500` em silêncio — e o segundo ainda não existe, o que está registrado em [06-riscos-e-limitacoes](../06-riscos-e-limitacoes.md).
- `Replayed` embrulha a rejeição em vez de substituí-la: `errors.As` alcança o token através do `Unwrap`, e o marcador de replay por cima.

## Onde está no código

- `internal/platform/problem/problem.go` — `retryable`, `defective`, `replayed`, `asksToRetry`, `isDefect`, `isReplay`.
- `internal/app/submitwager/submitwager.go` — `transient`, `defect`, `Replayed`.
- `internal/app/resolvereference/resolvereference.go` — `defect`.
