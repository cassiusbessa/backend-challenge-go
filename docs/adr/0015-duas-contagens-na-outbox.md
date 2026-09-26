# 0015. Duas contagens na linha da outbox: tentativa e recusa permanente

Status: aceita · 2026-09-26

## Contexto

A linha da outbox precisa responder duas perguntas: quando tentar de novo (backoff) e quando desistir (linha morta). A regra de saída diz: backoff de 1s, fator 2, teto 60s; morte na décima recusa permanente do broker.

## Opções consideradas

1. Uma contagem só, que move o backoff e decide a morte.
2. Duas contagens: `attempt_count` sobe em toda falha e move o backoff; `refusal_count` sobe só quando o broker recusou para valer, e decide a morte.

## Decisão

Opção 2. São duas perguntas, e contá-las juntas mataria linha sadia: um broker fora do ar por dois minutos deixa nove tentativas atrás de si, e a décima falha de rede seria lida como a décima recusa.

## Consequências

- Uma indisponibilidade longa do broker atrasa a publicação e não perde evento.
- Morta, a linha permanece com o mesmo `eventId`, visível no log e no banco, e sai da fila de publicação junto com a publicada — é o que faz os eventos seguintes daquela carteira seguirem.
- `published_at` e `dead_at` não coexistem, por `CHECK`.

## Onde está no código

- `deploy/migrations/000005_outbox_refusal_count.up.sql` — a segunda coluna.
- `internal/app/relayoutbox/relayoutbox.go` — `setBack`, `setAside`, `kill`.
- `internal/app/relayoutbox/backoff.go` — `Backoff`.
