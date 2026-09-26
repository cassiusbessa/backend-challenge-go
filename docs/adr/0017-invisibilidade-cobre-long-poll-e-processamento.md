# 0017. A invisibilidade da mensagem cobre o long poll mais o processamento, verificado na subida

Status: aceita · 2026-09-26

## Contexto

O consumidor tem três prazos: a espera do long poll (`QUEUE_POLL`), a invisibilidade da mensagem depois de recebida (`QUEUE_VISIBILITY`) e o prazo de decisão por mensagem (`QUEUE_TIMEOUT`). Cada um é um número positivo válido por si.

## Opções consideradas

1. Três números independentes, cada um validado só por presença e sinal.
2. A relação entre eles é uma invariante, e a subida recusa uma configuração que a viole.

## Decisão

Opção 2: `visibility > poll` e `poll + timeout <= visibility`.

Medido no broker local: com a invisibilidade menor que a espera do long poll, a busca devolve resposta vazia **e consome a entrega**. Sem erro, sem linha de log, sem sinal. Cinco turnos depois toda mensagem legítima está na DLQ sem nunca ter sido processada. O prazo de decisão fica abaixo do que resta pela mesma razão: a entrega que ele gasta tem de ainda estar invisível quando a resposta ao broker sai.

## Consequências

- Presença não basta, e por isso a verificação não está no `require` genérico da configuração: os três podem estar definidos e ainda nomear um processo que não consegue liquidar uma mensagem.
- Um valor fora da relação impede a subida em vez de cair no padrão.

## Onde está no código

- `internal/platform/config/config.go` — `ingressWindows`.
- `internal/platform/wagerqueue/consumer.go` — o comentário da medição.
