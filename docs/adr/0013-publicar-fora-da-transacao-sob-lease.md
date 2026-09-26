# 0013. Publicar fora de qualquer transação SQL, sob lease com token

Status: aceita · 2026-09-26

## Contexto

O relay lê a outbox e envia ao SNS. Várias réplicas fazem isso ao mesmo tempo, e a mesma linha não pode ser enviada por duas nem esquecida por todas. O envio é chamada de rede a um broker que pode estar lento ou fora.

## Opções consideradas

1. Reivindicar e publicar dentro de uma transação SQL, commitando depois do envio.
2. Publicar sem reivindicação, deduplicando só no consumidor pelo `eventId`.
3. Três tempos e duas transações curtas: reivindicar a linha gravando token e prazo de lease; publicar **fora** de qualquer transação; confirmar só se o token ainda for o da reivindicação.

## Decisão

Opção 3. A opção 1 prende uma conexão do pool pelo tempo de um broker indisponível — sob carga, o pool esgota e a API para de responder por causa de um componente de fundo. A opção 2 publica a mesma linha por todas as réplicas em todo turno.

O prazo do envio cabe dentro do lease, para a réplica não estar publicando depois de ter perdido a linha.

## Consequências

- A entrega é **ao menos uma vez**: um lease que vence com o envio em curso pode publicar duas vezes. Quem fecha essa janela é o `eventId` estável, do lado de quem consome, e a deduplicação do SNS FIFO por ele.
- Por carteira, publica-se só o evento não publicado mais antigo; carteiras diferentes não compartilham essa serialização, e `SKIP LOCKED` pula a linha que outra réplica segura.
- A confirmação de um envio que já passou pelo broker não toma o cancelamento do `SIGTERM`: uma confirmação que não aterrissa é uma republicação.

## Onde está no código

- `internal/app/relayoutbox/relayoutbox.go` — `Relay`, `turn`, `deliver`, `confirm`.
- `internal/platform/postgres/outbox.go` — a reivindicação e a confirmação condicionada ao token.
- `internal/platform/outboxrelay/outboxrelay.go` — o turno e a parada.
