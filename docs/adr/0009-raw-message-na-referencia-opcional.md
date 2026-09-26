# 0009. `json.RawMessage` no campo de referência do corpo

Status: aceita · 2026-09-26

## Contexto

`referenceExternalTransactionId` é o único campo do corpo que pode ser omitido: um `WIN` sem citação credita na hora, sem verificar aposta nenhuma. Um `WIN` com citação verifica que a aposta existe, foi `PROCESSED` e fecha com ele. A diferença entre os dois é dinheiro creditado com ou sem verificação.

## Opções consideradas

1. `string`: ausente, `null` e `""` colapsam em `""`.
2. `*string`: distingue ausente de presente; `null` vira `nil`; tipo errado falha o decode inteiro.
3. `json.RawMessage`: os bytes crus, decididos pela borda.

## Decisão

Opção 3. A borda precisa de **três** estados, não dois: ausente e `null` significam "não cita nada"; um valor presente e válido cita; um valor presente e inválido é recusa.

Com `string`, um provedor que **quis** citar `bet-99` e errou a grafia teria o corpo lido como "não cita nada" — e o `WIN` seria creditado sem verificação. É a única linha do decode onde um erro de digitação muda a classe da operação.

## Consequências

- Ausente e `null` são o mesmo caso, como `go-idempotency` já dizia do hash: `null` não entra no negócio.
- Um valor malformado responde `400` nomeando o campo, e nada acontece.
- É a mesma opcionalidade que faz `LOSS` não citar: a citação existe para quem precisa de um fato da citada para decidir dinheiro.

## Onde está no código

- `internal/platform/wagerapi/decode.go` — `submitRequest.ReferenceExternalTransactionID`, `fields.reference`.
- `internal/domain/wager/win.go` — o desvio sem citação.
