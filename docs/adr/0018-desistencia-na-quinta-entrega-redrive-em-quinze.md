# 0018. O consumidor desiste na quinta entrega; o redrive da fila fica em quinze

Status: aceita · 2026-09-26

## Contexto

Uma mensagem que falha de forma transitória volta para a fila com backoff de visibilidade. Em algum ponto é preciso desistir e copiá-la para a DLQ. O broker tem o próprio mecanismo de desistência, o `maxReceiveCount` do redrive. A fila é FIFO com grupo por carteira: uma mensagem com falha na cabeça do grupo segura as de trás.

## Opções consideradas

1. Deixar o broker desistir, com `maxReceiveCount` igual ao limite desejado.
2. O consumidor desiste na quinta entrega e copia para a DLQ; o `maxReceiveCount` do broker fica mais alto, em quinze.

## Decisão

Opção 2. O limite do consumidor é a decisão de negócio; o do broker é rede de segurança.

Eles são diferentes de propósito: as mensagens atrás de uma cabeça com falha também acumulam entregas enquanto esperam, porque o FIFO as devolve junto. Com o redrive igual ao limite da aplicação, o broker descartaria uma mensagem sadia antes de ela ter tido as suas cinco chances.

## Consequências

- A cópia para a DLQ é do consumidor, com a razão registrada num conjunto fechado de tokens, e a mensagem é apagada da fila de origem.
- O redrive do broker só age se o consumidor não agir — processo morto no meio de um turno, por exemplo.
- A janela de deduplicação de cinco minutos do FIFO não substitui a inbox ([ADR 0016](0016-inbox-em-savepoint.md)).

## Onde está no código

- `internal/platform/wagerqueue/consumer.go` — `deliveryLimit`, e o comentário sobre o grupo por carteira.
- `deploy/terraform/localstack/sqs.tf` — `maxReceiveCount = 15`.
