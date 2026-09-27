# 0040. Os spans do caso de uso e da unit of work nascem do span pai

Status: aceita · 2026-09-27

## Contexto

O trace de uma operação devia ter três spans: o da entrada, o do caso de uso e o da unit of work, fechado no commit. Só o primeiro existia. Um pedido aparecia no Tempo como um bloco único, sem dizer quanto do tempo foi decodificação, decisão ou banco. O domínio e os casos de uso não conhecem OpenTelemetry, e a unit of work é construída pelo grafo do Fx antes de qualquer pedido.

## Opções consideradas

1. Injetar o tracer no construtor da unit of work e de cada borda, pelo grafo do Fx.
2. Um decorador por caso de uso no composition root, implementando a interface que a borda consome.
3. Abrir o span no ponto de chamada, na borda e na unit of work, a partir do provider do span que o contexto já carrega.

## Decisão

Opção 3. A opção 1 muda a assinatura de construtores que o Fx resolve em tempo de execução, e daria à unit of work um tracer que ela usaria também no trabalho de fundo — o worker de referência abriria um trace órfão a cada espera que tenta fechar. A opção 2 pede um tipo por interface de borda, perto de dez, só para abrir e fechar um span.

Pelo provider do pai, o span filho fica no mesmo pipeline do span da entrada, e um contexto sem span abre o span no-op: o worker de referência, que não tem entrada, continua sem trace. O span filho marca erro só para a falha que carrega stack, infraestrutura e conflito de versão; a rejeição de regra o deixa ok, e a stack continua só no span da entrada, onde a borda a grava.

## Consequências

- Rota HTTP e mensagem da fila têm os três spans; os nomes são o verbo do caso de uso (`submit wager`, `open wallet`, `receive wager`…) e `unit of work`, com `db.system.name`.
- A outbox passa a gravar o `span_id` do caso de uso, que envolve o commit, e o link do relay aponta para ele.
- Nenhum tracer atravessa `internal/app`; a camada continua sem OpenTelemetry.
- **O que se paga:** o fechamento de uma espera pelo worker de referência não aparece no Tempo, porque não há entrada de que ele seja filho, e cada borda nova tem de lembrar de abrir o span do caso de uso, sem nada que a obrigue.

## Onde está no código

- `internal/platform/telemetry/span.go` — `Step`, o span filho pelo provider do pai, e `end`, o status pela stack.
- `internal/platform/postgres/uow.go` — `Within`, o span `unit of work`.
- `internal/platform/wagerapi/transactions.go`, `internal/platform/walletapi/wallets.go`, `ledger.go` e `reconciliation.go` — o span de cada caso de uso das rotas.
- `internal/platform/wagerqueue/consumer.go` — `settle`, o span `receive wager` sob o da mensagem.
