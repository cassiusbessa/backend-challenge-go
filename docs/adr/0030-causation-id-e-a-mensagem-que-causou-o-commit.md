# 0030. O `causationId` é a mensagem que causou o commit

Status: aceita · 2026-09-26

## Contexto

O envelope dos eventos leva `correlationId` e, quando houver, `causationId`. O exemplo de evento do enunciado mostra `"causationId": "transactionId-optional"`. O campo é o `messageId` da mensagem da fila cujo commit escreveu o evento, e fica ausente quando nenhuma mensagem o produziu: uma chegada por HTTP, a abertura de carteira, e a espera que o worker de referência fecha, que quem dispara é o prazo. O contrato HTTP passou a seguir o enunciado ([0029](0029-contrato-http-segue-o-enunciado.md)), e a pergunta é se o envelope de saída segue também.

## Opções consideradas

1. A transação como causa de todo evento: `causationId` recebe o `transactionId` da operação que o commit decidiu.
2. A mensagem como causa: `causationId` é o `messageId` da entrega que produziu o commit, e fica ausente quando nenhuma mensagem o produziu.

## Decisão

Opção 2. Os quatro eventos já levam `transactionId` em `data`, e o envelope leva a carteira em `aggregateId`. Com a opção 1 o campo seria uma cópia de `data.transactionId`, e o envelope perderia o único elo que não está em lugar nenhum: o que liga o evento à entrega que o provocou. Com a mensagem, quem consome o tópico chega do evento à mensagem da fila, e dali à linha da inbox e, se for o caso, à DLQ. O `"transactionId-optional"` do exemplo é lido como ilustrativo: o sufixo diz que o campo é opcional, e o que se põe nele é a causa imediata.

A espera fechada pelo worker não herda o `messageId` da mensagem que a abriu. Emprestá-lo diria que aquela mensagem causou um evento que o prazo causou, minutos depois e em outro commit.

## Consequências

- Os eventos de um commit que veio da fila apontam a mesma entrega; uma reentrega que vira replay não escreve evento novo, então não há segundo evento apontando a segunda entrega.
- Evento de chegada por HTTP, de abertura e de espera fechada pelo prazo sai sem `causationId` — omitido, não vazio.
- **O que se paga:** quem lê o exemplo ao pé da letra espera a transação no campo e encontra uma mensagem, ou nada. A transação de um evento está sempre em `data`, e não no envelope.

## Onde está no código

- `internal/platform/wagerqueue/reporter.go` — `Reporter.Receiving`, que põe o `messageId` como causa no contexto da entrega.
- `internal/platform/telemetry/log.go` — `WithCausation` e `Causation`, o transporte da causa até o commit.
- `internal/platform/postgres/outbox.go` — `outbox.Insert`, que grava a causa na origem do evento.
- `internal/domain/event/envelope.go` — `wire.CausationID`, omitido quando não há causa.
