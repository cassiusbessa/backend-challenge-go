# 0007. Catálogo fechado de `failureCode`, e a colisão de carteira fora dele

Status: aceita · 2026-09-26

## Contexto

Uma rejeição de negócio carrega um token estável que o provedor programa contra. HTTP e SQS têm de devolver o mesmo token. A borda precisa de um status por token, e a observabilidade de uma série Prometheus por token. Abrir uma segunda carteira para o mesmo jogador e moeda também é uma recusa — e não é uma aposta.

## Opções consideradas

1. Token como `string` livre, criado no ponto de uso.
2. Catálogo fechado em `wager.FailureCode`, enumerável, com `Catalog()`.
3. Catálogo fechado, e um token `WALLET_EXISTS` acrescentado para a colisão de carteira.

## Decisão

Opção 2, com a colisão de carteira **fora** do catálogo.

Um token inventado no ponto de uso chega ao contrato externo sem status e sem série. O catálogo enumerável é o que permite o mapa de status e a lista de séries serem exaustivos — e o teste de exaustividade percorrer `Catalog()`.

A colisão de carteira recusa um `OpenWallet`, não uma aposta. Ela sai como `409` com `type` próprio e sem `failureCode`. Acrescentá-la ao catálogo abriria a porta para cada borda inventar o seu, que é o que a lista existe para impedir.

## Consequências

- `NewRejection` recusa um código fora do catálogo e devolve `ErrUnknownFailureCode`, que **não** é uma `Rejection` — o chamador não confunde os dois.
- A carteira responde uma condição; quem nomeia o token é a ação, porque a mesma condição é `INSUFFICIENT_FUNDS` numa aposta e `REVERSAL_INSUFFICIENT_FUNDS` numa reversão.
- `problem.Class` tem `WalletExists` como classe própria, e `storage.ErrWalletExists` é sentinela sem token.

## Onde está no código

- `internal/domain/wager/failure.go` — `FailureCode`, `failureTokens`, `Catalog`, `NewRejection`.
- `internal/domain/wager/action.go` — `translate`: condição da carteira vira token.
- `internal/platform/problem/problem.go` — `WalletExists`, `namedClassOf`.
- `internal/app/storage/storage.go` — `ErrWalletExists`.
