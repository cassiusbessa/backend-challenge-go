# 0012. O flush da telemetria acontece fora do ciclo de vida do Fx, com orçamento próprio

Status: aceita · 2026-09-26

## Contexto

O Fx entrega a **todos** os hooks de parada o mesmo contexto do `Stop`, e retorna no instante em que esse contexto vence — pulando as paradas que ainda não alcançou. Uma parada que consome o orçamento inteiro não só atrasa: leva as de trás consigo. O flush do buffer de traces e logs, registrado como hook, seria a primeira coisa perdida num encerramento que estourou — justamente o encerramento que o trace precisaria contar.

## Opções consideradas

1. Flush como último hook `OnStop`.
2. Limitar o `Shutdown` do pipeline por dentro, com prazo próprio.
3. Flush **depois** do ciclo de vida do Fx, com orçamento separado de 3 segundos.

## Decisão

Opção 3. A opção 1 é pulada exatamente quando importa. A opção 2 não resolve, porque o hook nunca é chamado — não há o que limitar.

Junto com isso, cada parada do ciclo de vida recebe uma **fatia** do `SHUTDOWN_TIMEOUT`, calculada ao lado do registro, porque nenhum componente sabe o total nem quantas paradas vêm depois dele. A subida é recusada se o orçamento não paga a soma das fatias, em vez de isso ser descoberto no único encerramento que tinha algo a relatar.

## Consequências

- O Compose dá 30 segundos antes do `SIGKILL`, e o `terminationGracePeriodSeconds` do Kubernetes cobre o orçamento mais a reserva do flush.
- Telemetria que não conseguiu sair é registrada no log e não faz o processo sair com erro.
- A maior fatia é a do consumidor da fila, e ela não é um número escrito no registro: ele responde o próprio orçamento — o prazo de uma mensagem mais a janela da resposta ao broker — para a fatia não derivar dos dois valores que a decidem.

## Onde está no código

- `internal/platform/app/app.go` — `register`, `within`, as fatias.
- `internal/platform/telemetry/otel.go` — o `Shutdown` do pipeline.
- `internal/platform/config/config.go` — `SHUTDOWN_TIMEOUT`.
