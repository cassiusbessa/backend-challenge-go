# 0011. Três componentes de fundo separados, sem abstração comum

Status: aceita · 2026-09-26

## Contexto

O binário tem três processos de fundo: o worker de referência, o relay da outbox e o consumidor da fila. Os dois primeiros têm ticker, lote e parada parecidos — perto de vinte linhas em comum. A tentação é um runner genérico.

## Opções consideradas

1. Um `Runner` comum, parametrizado por intervalo e por uma função de turno.
2. Três tipos independentes, cada um com o próprio ciclo.

## Decisão

Opção 2. Os turnos têm formas diferentes, e a abstração teria de expor para um o que esconde do outro.

O turno do worker de referência é **uma transação SQL**. O do relay é **banco, rede, banco**, com um lease no meio e a publicação fora de qualquer transação — o runner comum teria de saber do lease. O do consumidor é **long poll bloqueante** com decisão por mensagem, e ele nem aceitaria o parâmetro de intervalo que os outros dois compartilham.

## Consequências

- Vinte linhas de ticker e parada duplicadas entre dois componentes. É o preço, e é pequeno.
- Cada um sobe e desce no lifecycle com a própria fatia de prazo, e a parada de cada um tem semântica própria: o worker para de reivindicar, o relay conclui o envio em curso dentro do lease, o consumidor conclui a decisão em curso e devolve a mensagem cortada com visibilidade zero.
- O terceiro, sendo tão diferente, é evidência a favor da decisão, não contra.

## Onde está no código

- `internal/platform/referenceworker/referenceworker.go`
- `internal/platform/outboxrelay/outboxrelay.go`
- `internal/platform/wagerqueue/consumer.go`
- `internal/platform/app/app.go` — `register`: as fatias de parada.
