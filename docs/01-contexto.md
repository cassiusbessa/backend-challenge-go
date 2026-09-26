# Contexto e containers

O serviço é um bounded context, um binário e N réplicas. Esta página é o nível 1 e o nível 2 do modelo C4: quem fala com o sistema, e do que o sistema é feito por dentro.

## Nível 1 — o sistema e quem o cerca

```mermaid
flowchart LR
    provider(["Provedor de jogo<br/>envia apostas · lê a própria transação"])
    internal(["Cliente interno<br/>abre, lê e reconcilia carteira"])
    consumer(["Consumidor de eventos<br/>ainda não existe"])

    system["<b>Liquidação de apostas</b><br/>Go · um binário · N réplicas"]

    keycloak["Keycloak<br/>emite tokens client_credentials"]
    pg[("PostgreSQL<br/>carteiras · transações · ledger<br/>outbox · inbox")]
    sqs["SQS FIFO<br/>wager-transactions.fifo<br/>wager-transactions-dlq.fifo"]
    sns["SNS FIFO<br/>wallet-events.fifo"]
    otel["OTel Collector<br/>→ Tempo · Loki · Prometheus → Grafana"]

    provider -- "HTTP + Bearer" --> system
    internal -- "HTTP + Bearer" --> system
    provider -- "mensagem FIFO<br/>grupo = carteira" --> sqs
    sqs -- "long poll" --> system
    provider -. "obtém token" .-> keycloak
    system -- "JWKS, sob demanda" --> keycloak
    system <--> pg
    system -- "publica" --> sns
    sns -. "assinatura futura" .-> consumer
    system -- "OTLP · scrape de /metrics" --> otel
```

Duas identidades entram pelo HTTP, e a diferença entre elas é papel, não credencial: o **provedor** envia apostas e lê só a própria transação; o **cliente interno** abre, lê e reconcilia carteira e não envia aposta. Quem decide é o cliente do token cruzado com um mapa versionado — o `providerId` do corpo não autoriza nada.

## Os sistemas externos

| Sistema | Protocolo | O que consumimos | Se cai |
| --- | --- | --- | --- |
| **PostgreSQL** | `pgx`, pool único por processo | tudo que é durável | `/health/ready` falha; a API responde `503` |
| **Keycloak** | HTTPS, JWKS | a chave pública do realm, buscada na primeira verificação e cacheada com rotação | o processo sobe e responde `401` até a chave chegar; readiness **não** o cobre ([ADR 0020](adr/0020-jwks-separado-do-issuer.md)) |
| **SQS** | AWS SDK, long poll | `wager-transactions.fifo` como entrada; a DLQ como destino de abandono | `/health/ready` falha; o consumidor tenta de novo |
| **SNS** | AWS SDK | `wallet-events.fifo` como destino dos eventos | a outbox acumula com backoff; nada se perde ([ADR 0015](adr/0015-duas-contagens-na-outbox.md)) |
| **OTel Collector** | OTLP/gRPC | destino de traces e logs | o buffer descarta; o processo não para |
| **Prometheus** | scrape de `/metrics` | — | a série tem buraco |

Localmente, SQS e SNS são o **LocalStack**, e o Keycloak é um realm de teste em `deploy/keycloak/`. O Terraform em `deploy/terraform/localstack/` descreve as filas, o tópico e o papel IAM do remetente; o `apply` de desenvolvimento aponta para o LocalStack.

## Nível 2 — o que há dentro do binário

```mermaid
flowchart TB
    subgraph process["Processo · uma réplica"]
        direction TB
        api["API HTTP<br/>HTTP_ADDR · rotas de carteira e aposta<br/>health · metrics"]
        pprof["pprof<br/>PPROF_ADDR, fora da porta da API"]
        ref["Worker de referência<br/>fecha as esperas pelo prazo"]
        relay["Relay da outbox<br/>publica por carteira, sob lease"]
        cons["Consumidor da fila<br/>long poll · inbox · DLQ"]
        watch["Observador de divergência<br/>varre as carteiras por página · só lê"]
        uc["Casos de uso<br/>openwallet · submitwager · resolvereference<br/>relayoutbox · receivewager<br/>readwallet · readwager · listledger · reconcilewallet"]
        dom["Domínio<br/>money · identity · wallet · ledger · wager · event"]
        pool["Pool pgx<br/>SET ROLE wager_app"]
    end
    migrate["migrate<br/>Job que roda uma vez antes das réplicas"]
    pg[("PostgreSQL")]
    sqs["SQS"]
    sns["SNS"]
    kc["Keycloak"]

    api --> uc
    ref --> uc
    relay --> uc
    cons --> uc
    watch --> uc
    uc --> dom
    uc --> pool
    pool --> pg
    migrate --> pg
    cons -- "Receive · Delete" --> sqs
    relay -- "Publish" --> sns
    api -. "JWKS" .-> kc
```

Os quatro componentes de fundo são tipos separados, não quatro usos de um runner comum ([ADR 0011](adr/0011-tres-runners-de-fundo-separados.md)). Todos sobem no mesmo ciclo de vida do Fx, depois do pool e antes do listener, e descem na ordem inversa: a porta deixa de aceitar primeiro, e os quatro param de reivindicar, buscar ou ler depois dela, cada um dentro da própria fatia do prazo ([05-transversais](05-transversais.md#ciclo-de-vida)).

O observador de divergência é o único dos quatro que não escreve. Ele varre todas as carteiras em páginas, a partir de um cursor em memória, e entrega cada uma ao mesmo `reconcilewallet` que a rota chama: a divergência que ele existe para achar é a que nenhuma escrita da aplicação produziu, e por isso nenhum filtro por atualização a encontraria ([ADR 0024](adr/0024-observador-de-divergencia-por-cursor-em-memoria.md)).

O binário não carrega migration. O SQL versionado é aplicado por um serviço do Compose que termina, ou por um Job do Kubernetes, antes das réplicas.

## As três camadas, e a direção que nunca inverte

```mermaid
flowchart TB
    subgraph platform["<b>internal/platform</b> — adaptadores: implementam as portas e injetam"]
        direction LR
        entrada["<b>Bordas de entrada</b><br/>httpapi · wagerapi · walletapi · authz<br/>wagerqueue · referenceworker · outboxrelay · divergencewatch"]
        saida["<b>Adaptadores de saída</b><br/>postgres · broker · telemetry · metrics<br/>mint · clock"]
        erro["<b>Contrato de erro</b><br/>problem · fault"]
        root["<b>Composition root</b><br/>app · config"]
    end

    subgraph application["<b>internal/app</b> — casos de uso: coordenam, e declaram as portas que precisam"]
        direction LR
        storage["<b>Portas</b><br/>storage: UnitOfWork · Tx · Reads<br/>Wallets · Transactions · Entries · Outbox · Inbox"]
        escrita["<b>Escrita</b><br/>openwallet · submitwager · receivewager<br/>resolvereference · relayoutbox"]
        leitura["<b>Leitura</b><br/>readwallet · readwager<br/>listledger · reconcilewallet"]
        apoio["<b>Apoio</b><br/>bodyhash · referencewait"]
    end

    subgraph domain["<b>internal/domain</b> — decide: biblioteca padrão e nada mais"]
        direction LR
        money["money"]
        identity["identity"]
        wallet["wallet"]
        ledger["ledger"]
        wager["wager"]
        event["event"]
    end

    platform == "implementa storage.* · injeta Minter, Clock, Schedule" ==> application
    application == "chama Open, Debit, Credit, Bet, Win… · lê wager.Rejection" ==> domain

    classDef layer fill:#f7f7f7,stroke:#999,color:#222
    classDef box fill:#fff,stroke:#666,color:#222
    class platform,application,domain layer
    class entrada,saida,erro,root,storage,escrita,leitura,apoio,money,identity,wallet,ledger,wager,event box
```

`domain` importa a biblioteca padrão e a si mesmo. `app` importa `domain` e declara as portas que precisa — `storage.UnitOfWork`, `submitwager.Minter`, `submitwager.Clock` — sem nomear quem as implementa. `platform` implementa e injeta; `app` é o único pacote que conhece o Fx. A seta nunca aponta para cima: um caso de uso nunca nomeia `pgx`, e um teste de unidade nunca precisa de um banco.

## Contratos de fronteira

**HTTP.** Nove rotas, e a lista fecha aqui:

| Rota | Papel | Responde |
| --- | --- | --- |
| `POST /wallets` | interno | `201` |
| `GET /wallets/{walletId}` | interno | `200` |
| `GET /wallets/{walletId}/ledger` | interno | `200`, página com `nextCursor` quando há próxima |
| `GET /wallets/{walletId}/reconciliation` | interno | `200`, `consistent` e o vocabulário de divergência |
| `POST /wagering/transactions` | provedor | `201` decidida · `202` esperando · `200` replay |
| `GET /wagering/transactions/{transactionId}` | provedor, só a própria | `200` |
| `GET /health/live` · `GET /health/ready` | público | `200` / `503` |
| `GET /metrics` | público | Prometheus |

Dinheiro entra e sai como `{"amount":"25.00","currency":"BRL"}` — string decimal de duas casas, nunca número JSON. Toda recusa sai como `application/problem+json` (RFC 9457), com `type` nomeando a classe do problema e `failureCode` numa extensão nomeando a regra. A chave de idempotência chega em `Idempotency-Key`.

**Fila.** O mesmo negócio chega em `data`, com a chave em `data.idempotencyKey`; `MessageGroupId` é a carteira em minúsculas e `MessageDeduplicationId` é o `messageId` do envelope. HTTP e fila produzem o mesmo hash do corpo de negócio.

**Eventos.** Quatro, e só estes: `WagerTransactionProcessed`, `WagerTransactionRejected`, `WagerTransactionPendingReference`, `WalletBalanceChanged`. O envelope leva `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` quando houver, `occurredAt` em UTC e `data`, com `version` 1. Republicação reutiliza o `eventId`, e o payload não muda.

## Configuração

Tudo por variável de ambiente, lida e validada antes de qualquer porta abrir. Ausente ou malformada derruba a subida, não o primeiro pedido.

| Grupo | Variáveis |
| --- | --- |
| Processo | `HTTP_ADDR` · `PPROF_ADDR` · `SHUTDOWN_TIMEOUT` |
| Banco | `DATABASE_URL` |
| Identidade | `IDP_ISSUER` · `IDP_JWKS_URL` · `CLIENTS_PATH` |
| Fila de entrada | `SQS_ENDPOINT` · `SQS_QUEUE_URL` · `SQS_DLQ_URL` · `QUEUE_SENDERS_PATH` · `QUEUE_POLL` · `QUEUE_VISIBILITY` · `QUEUE_TIMEOUT` |
| Eventos | `SNS_ENDPOINT` · `SNS_TOPIC_ARN` · `OUTBOX_INTERVAL` · `OUTBOX_LEASE` |
| Espera | `REFERENCE_TTL` · `REFERENCE_INTERVAL` |
| Reconciliação | `RECONCILIATION_INTERVAL` · `RECONCILIATION_BATCH` |
| Telemetria | `OTEL_EXPORTER_OTLP_ENDPOINT` · `OTEL_SAMPLE_RATIO` |

Os três prazos da fila não são três números: `QUEUE_VISIBILITY` tem de cobrir `QUEUE_POLL` mais `QUEUE_TIMEOUT`, e a subida recusa o contrário ([ADR 0017](adr/0017-invisibilidade-cobre-long-poll-e-processamento.md)). Os mapas de clientes e de remetentes são arquivos versionados, apontados por caminho.

## Implantação

`docker compose up --build` sobe o ambiente inteiro: `postgres`, `localstack`, `keycloak`, `otel-collector`, `tempo`, `loki`, `prometheus`, `grafana`, o `migrate` que termina, o `tools` do Terraform e `wager`, uma réplica do processo. As três instâncias que o desafio pede ficam para Kind ou k3d, com a migration como Job que roda uma vez antes das réplicas; os manifestos e o guia ainda não existem, e [06 · Riscos e limitações](06-riscos-e-limitacoes.md) os lista como pendentes.
