# 0038. O relay envia por várias carteiras ao mesmo tempo e encadeia cada uma

Status: aceita · 2026-09-26

## Contexto

O relay publica, por carteira, só o evento não publicado mais antigo, e o seguinte daquela carteira só depois de confirmar o anterior ([0013](0013-publicar-fora-da-transacao-sob-lease.md)). Cada réplica varria 50 candidatos por tique de 1 s e os enviava em série. A primeira carga sobre três réplicas mediu ~110 eventos/s drenados contra ~800/s gravados, com o PostgreSQL a 113% de CPU e as réplicas quase ociosas: o publish no LocalStack leva 5–20 ms, e o que pesava era a varredura, com `NOT EXISTS` sobre todos os pendentes, repetida a cada evento de uma carteira quente — ~36 ms com 4 mil pendentes, ~83 ms com 12 mil.

## Opções consideradas

1. Manter o envio em série e esperar a outbox drenar depois da carga.
2. Aumentar o lote ou encurtar o tique.
3. Enviar ao mesmo tempo os candidatos de uma varredura, e, depois de cada envio que passou, pedir o próximo evento da mesma carteira pela própria carteira, sem nova varredura.
4. Publicar em lote por carteira: reivindicar os próximos dez eventos de uma carteira e enviá-los numa chamada, que o SNS FIFO entrega em ordem.

## Decisão

Opção 3. Manter o série deixa o sistema drenando um oitavo do que grava, e a varredura consumindo o banco que a liquidação usa. Lote maior ou tique menor aumentam a varredura, que já era o custo, e não mexem no teto de uma carteira. Publicar em lote multiplicaria esse teto, mas quebra a regra de não começar o seguinte antes de confirmar o anterior, e o claim e a confirmação passam a ser por lote, com falha parcial a decidir: é outro change.

A varredura já escolhe no máximo um evento por carteira, então os candidatos de uma varredura são de carteiras diferentes e podem sair juntos, até 8 por réplica, sem tocar a ordem de nenhuma. Depois de um envio, `NextOf` lê a cabeça daquela carteira pelo índice parcial `(wallet_id, publish_seq)` e só a responde se ela puder ser reivindicada agora: uma cabeça sob lease de outra réplica, ou devolvida ao backoff, responde nada — nunca o evento de trás. O encadeamento para em 50 eventos por carteira, para uma carteira que nunca se esgota devolver o lugar. A varredura cheia se repete sem esperar o tique, e o backlog é medido depois de cada varredura, porque um turno longo congelava os gauges da outbox.

## Consequências

- Medido depois: a vazão HTTP da mesma carga subiu de ~570 para ~1 900 chegadas decididas/s, porque o banco deixou de ser consumido pela varredura; cada carteira passou a drenar ~70–95 eventos/s, contra ~15/s antes.
- A ordem por carteira e a deduplicação pelo `eventId` continuam as do 0013, e o cenário dos publishers concorrentes continua publicando cada evento uma vez.
- **O que se paga:** o teto de uma carteira é o de um envio por vez, ~10 ms por evento contra o LocalStack. Na carga sintética, cada uma das quatro carteiras quentes recebe ~200 eventos/s, então a outbox delas drena minutos depois da janela, e o alerta de outbox parada dispara durante a carga. Um envio de cada vez por carteira é o preço da ordem; o passo seguinte, se uma carteira real chegar perto disso, é a opção 4.

## Onde está no código

- `internal/platform/outboxrelay/outboxrelay.go` — `batch`, `inFlight`, `perWallet`, `turn`, `relayAll` e `chain`.
- `internal/platform/postgres/outboxqueue.go` — `selectNextOutboxEvent` e `NextOf`.
- `internal/app/storage/outbox.go` — `NextOf` na porta `OutboxQueue`.
