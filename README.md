# Liquidação de apostas

Serviço em Go que liquida as operações de provedores de jogo — aposta, ganho, perda, estorno e cancelamento — sobre a carteira do jogador. Cada movimento vira um lançamento num ledger imutável, a mesma operação enviada duas vezes produz um efeito só, e o desfecho é publicado como evento depois do commit.

As operações chegam por HTTP ou por uma fila SQS FIFO, e o serviço roda em N réplicas iguais sobre um PostgreSQL compartilhado — no Docker Compose ou num cluster Kubernetes local.

## O que o serviço garante

- **Saldo nunca negativo**, mesmo com réplicas disputando a mesma carteira: lock da linha antes de decidir, versão como guarda do `UPDATE` e `CHECK` no banco.
- **Uma operação, um efeito.** A idempotência é decidida por índice único, não por consulta prévia. HTTP e fila produzem o mesmo hash, então a mesma operação pelos dois canais é reconhecida como uma só.
- **Ledger imutável.** Todo movimento tem um lançamento que o explica, a tabela só aceita `INSERT`, e no commit o saldo da carteira é o saldo posterior do último lançamento.
- **Nenhum evento antes do commit.** O evento entra na outbox na mesma transação SQL do saldo; um relay publica depois, ao menos uma vez, com `eventId` estável.
- **Operação fora de ordem espera.** Um estorno que chega antes da aposta que cita fica em `PENDING_REFERENCE` e é concluído — ou rejeitado por prazo — por um worker.
- **Reconciliação contínua.** Um observador compara saldo e ledger de cada carteira e dispara alerta na primeira divergência.
- **Dinheiro sem `float`.** `int64` de centavos com a moeda no tipo; entra e sai como string decimal.

## Além do enunciado

O desafio lista tracing, dashboards e teste de carga como diferenciais opcionais. Os três estão aqui, junto com o que faz o serviço rodar e ser verificado como um sistema de produção:

- **Réplicas atrás de um endereço só.** Três réplicas do mesmo binário atrás de um HAProxy que só encaminha para a réplica pronta. O mesmo conjunto sobe num cluster Kubernetes local (Kind), com a migration como Job, por `make cluster-up`.
- **Infraestrutura como código.** Terraform descreve as filas FIFO, a DLQ com redrive, o tópico SNS FIFO e o remetente IAM, e roda como serviço do Compose — sem Terraform no host. Os manifests Kubernetes estão em [`deploy/k8s`](deploy/k8s).
- **Observabilidade de ponta a ponta.** Traces, logs e métricas por OpenTelemetry até Tempo, Loki e Prometheus. O painel "Liquidação" é provisionado como código e abre o trace de cada ponto do p99; as três regras de alerta têm teste de unidade.
- **Carga com veredito de consistência.** `make load` mede vazão, p50/p95/p99, erros, conflitos de versão e atraso da outbox — os campos que o enunciado pede — e termina conferindo saldo, versão e lançamentos de cada carteira, inclusive com uma réplica morta no meio da janela.
- **Falhas exercitadas, não só descritas.** Réplica morta sob carga, banco, broker e IdP pausados, e divergência gravada por fora da aplicação, cada exercício com o que se observou em [07 · Operação](docs/07-operacao.md#exercícios-de-falha).
- **O CI como portão.** `golangci-lint` com teto de complexidade ciclomática 6, `-race`, piso de cobertura por camada, suíte de integração contra PostgreSQL, Keycloak e LocalStack reais, e carga sobre as réplicas do Compose e do cluster. Teste de mutação sob comando, com `make mutation`.
- **Cada decisão registrada.** 40 ADRs, cada um com a alternativa que rejeitou, e a estrutura documentada com diagramas C4 em Mermaid.

| Em números | |
| --- | --- |
| Testes | 1.119 de unidade e 177 de integração, estes contra a infraestrutura real |
| Cobertura de unidade | 92,4% no total — domínio 95,8%, casos de uso 94,0%, adaptadores 90,7% |
| Cenários do enunciado | 8 de 8 automatizados, cada um sobre três ou mais instâncias independentes |
| Carga, três réplicas | até ~1.900 operações/s, p99 entre 89 e 135 ms, zero erros e zero conflitos de versão — inclusive matando uma réplica |
| Decisões | 40 ADRs |

## Arquitetura

```mermaid
flowchart LR
    http["HTTP<br/>Bearer + Idempotency-Key"]
    sqs["SQS FIFO<br/>grupo = carteira"]
    subgraph replica["Réplica × N"]
        direction TB
        border["Borda<br/>autentica · autoriza"]
        uc["Caso de uso"]
        dom["Domínio<br/>Wallet · Ledger · Wager"]
        uow["Unit of work — um commit<br/>inbox · carteira · transação<br/>lançamento · outbox"]
        workers["Em segundo plano<br/>relay da outbox · worker de referência<br/>observador de divergência"]
    end
    pg[("PostgreSQL")]
    sns["SNS FIFO<br/>wallet-events"]

    http --> border
    sqs --> border
    border --> uc --> dom --> uow --> pg
    workers <--> pg
    workers --> sns
```

Portas e adaptadores em três camadas: `internal/domain` guarda as regras e importa só a biblioteca padrão; `internal/app` coordena os casos de uso e declara as portas que precisa; `internal/platform` implementa essas portas e é a única camada que conhece `pgx`, HTTP, AWS e Uber Fx. Tudo que duas réplicas poderiam decidir diferente é decidido pelo PostgreSQL.

A visão completa está em [ARCHITECTURE.md](ARCHITECTURE.md), e cada decisão de desenho, com a alternativa rejeitada, em [docs/adr](docs/adr/README.md).

| | |
| --- | --- |
| Linguagem | Go 1.27 · Uber Fx |
| Persistência | PostgreSQL 16 · pgx · golang-migrate |
| Mensageria | SQS FIFO e SNS FIFO — LocalStack no ambiente local, provisionado por Terraform |
| Identidade | Keycloak, `client_credentials` |
| Observabilidade | OpenTelemetry → Tempo, Loki e Prometheus → Grafana |
| Réplicas | HAProxy no Compose · Kind para Kubernetes |

## Início rápido

**Pré-requisitos:** Docker com Compose 2.24+ e as portas `5432`, `4566`, `8080`, `8090`, `4317`, `4318`, `3000`, `3100`, `3200` e `9095` livres. Para testes, cenários e carga: Go 1.27.1 e `make`. Os exemplos abaixo usam `jq`.

```bash
docker compose up --build        # ou: make up
```

Não é preciso `.env`: os defaults são os valores locais de [.env.example](.env.example). A subida aplica o schema e provisiona filas e tópico no LocalStack antes de qualquer réplica, e sobe três réplicas atrás de um balanceador. Pronto quando:

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8090/health/ready   # 200
```

| Serviço | Endereço | Acesso |
| --- | --- | --- |
| API (balanceador → réplicas) | `http://localhost:8090` | token do Keycloak |
| Keycloak | `http://localhost:8080` | realm `junglegaming` |
| Grafana | `http://localhost:3000` | `admin` / `admin` |
| Prometheus | `http://localhost:9095` | — |
| PostgreSQL | `localhost:5432` | `junglegaming` / `junglegaming` |
| LocalStack (SQS e SNS) | `http://localhost:4566` | — |
| Coletor OTLP | `localhost:4317` (gRPC) · `localhost:4318` (HTTP) | — |

## API

| Rota | Quem chama | Sucesso |
| --- | --- | --- |
| `POST /wallets` | cliente interno | `201` |
| `GET /wallets/{walletId}` | cliente interno | `200` |
| `GET /wallets/{walletId}/ledger` | cliente interno | `200` |
| `POST /wallets/{walletId}/reconciliation` | cliente interno | `200` |
| `POST /wagering/transactions` | provedor | `201` concluída · `202` aguardando referência · `200` replay |
| `GET /wagering/transactions/{transactionId}` | provedor dono | `200` |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | provedor dono | `200` |
| `GET /health/live` · `GET /health/ready` · `GET /metrics` | público | `200` |

Dinheiro entra e sai como `{"amount":"25.00","currency":"BRL"}` — string decimal de duas casas, nunca número JSON. Todo erro sai em `application/problem+json` ([RFC 9457](https://www.rfc-editor.org/rfc/rfc9457)).

### Token

O Keycloak emite tokens por `client_credentials`. `wallet-internal` abre, lê e reconcilia carteiras; `provider-a` e `provider-b` enviam operações e leem só as próprias. Quem autoriza é o cliente do token — o `providerId` do corpo ou da URL não autoriza nada.

```bash
token() {
  curl -s http://localhost:8080/realms/junglegaming/protocol/openid-connect/token \
    -d grant_type=client_credentials -d client_id="$1" -d client_secret="$1-local" \
    | jq -r .access_token
}
INTERNAL=$(token wallet-internal)
PROVIDER=$(token provider-a)
```

### Abrir uma carteira

```bash
PLAYER_ID=$(uuidgen | tr 'A-Z' 'a-z')

WALLET_ID=$(curl -s -X POST http://localhost:8090/wallets \
  -H "Authorization: Bearer $INTERNAL" -H 'Content-Type: application/json' \
  -d '{"playerId":"'"$PLAYER_ID"'","initialBalance":{"amount":"1000.00","currency":"BRL"}}' \
  | jq -r .id)
```

```json
{"id": "01a0e07b-5576-…", "playerId": "…", "balance": {"amount":"1000.00","currency":"BRL"}, "version": 1}
```

Saldo inicial positivo grava, no mesmo commit, a carteira, a transação `OPENING` e o lançamento de crédito. Há uma carteira por jogador e moeda: a segunda responde `409`.

### Enviar uma operação

`kind` é `BET`, `WIN`, `LOSS`, `REFUND` ou `ROLLBACK`. O cabeçalho `Idempotency-Key` é obrigatório.

```bash
curl -si -X POST http://localhost:8090/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: bet-$WALLET_ID" \
  -d @- <<JSON
{"providerId":"provider-a","externalTransactionId":"bet-$WALLET_ID",
 "playerId":"$PLAYER_ID","walletId":"$WALLET_ID",
 "roundId":"round-1","gameId":"crash","kind":"BET",
 "money":{"amount":"25.00","currency":"BRL"}}
JSON
```

```
HTTP/1.1 201 Created
Location: /wagering/transactions/01a0e07b-5597-…
```

```json
{
  "transactionId": "01a0e07b-5597-…",
  "kind": "BET",
  "status": "PROCESSED",
  "externalTransactionId": "bet-…",
  "money": {"amount":"25.00","currency":"BRL"},
  "balance": {"amount":"975.00","currency":"BRL"},
  "idempotentReplay": false
}
```

`balance` é o saldo observado no commit. `BET` debita, `WIN` credita, `LOSS` conclui sem lançamento e sem mudar a versão da carteira. Reenviar a mesma chave com o mesmo corpo responde `200` com `idempotentReplay: true` e o saldo daquela época, não o atual.

### Respostas e `failureCode`

| Status | Quando |
| --- | --- |
| `400` | corpo inválido ou sem `Idempotency-Key` — nada é gravado |
| `401` | credencial ausente, inválida ou expirada |
| `403` | cliente sem permissão para a rota, ou corpo declarando outro provedor |
| `404` | recurso inexistente — ou de outro provedor, sem revelar que existe |
| `409` | segunda carteira do mesmo jogador na mesma moeda |
| `422` | regra de negócio recusou; o token vem na extensão `failureCode` |
| `503` | nenhuma réplica pronta — banco ou fila fora |

```json
{
  "type": "urn:junglegaming:problem:business-rejection",
  "title": "Wager rejected",
  "status": 422,
  "instance": "/wagering/transactions",
  "failureCode": "INSUFFICIENT_FUNDS",
  "transactionId": "01a0e07b-55bb-…"
}
```

| `failureCode` | Efeito |
| --- | --- |
| `INSUFFICIENT_FUNDS` · `REVERSAL_INSUFFICIENT_FUNDS` · `PLAYER_WALLET_MISMATCH` · `CURRENCY_MISMATCH` · `REVERSAL_AMOUNT_MISMATCH` · `REFERENCE_MISMATCH` · `REFERENCE_UNSUCCESSFUL` · `ALREADY_REVERSED` | grava a transação `REJECTED`, sem lançamento; o corpo traz o `transactionId` |
| `REFERENCE_NOT_FOUND` · `REFERENCE_NOT_PROCESSED` | encerram por prazo uma espera por referência |
| `WALLET_NOT_FOUND` · `OPENING_NOT_ALLOWED` · `AMOUNT_NOT_ALLOWED_FOR_KIND` · `REFERENCE_REQUIRED` | recusa sem gravar linha |
| `IDEMPOTENCY_CONFLICT` · `DUPLICATE_EXTERNAL_TRANSACTION` | mesma chave com outro corpo, ou mesmo id externo com outra chave; nenhuma linha nova |

A rejeição gravada ocupa a chave de idempotência: reenviar devolve a mesma recusa como replay, e tentar de novo de verdade exige chave nova. O `status` do corpo é o código HTTP, como a RFC define; o estado `REJECTED` fica legível na consulta pelo `transactionId` ([ADR 0029](docs/adr/0029-contrato-http-segue-o-enunciado.md)).

### Operações que citam outra

`REFUND` estorna um `BET`, e `ROLLBACK` reverte um `WIN` ou um `REFUND`, sempre pelo valor integral; um `WIN` também pode citar a aposta. A citação vai em `referenceExternalTransactionId`, e cada operação aceita uma única reversão.

Se a operação citada já está `PROCESSED`, tudo se decide no mesmo commit. Se ainda não chegou, a submissão responde **`202`** com `status: PENDING_REFERENCE`, e o worker de referência a conclui:

| A operação citada… | Desfecho |
| --- | --- |
| chega `PROCESSED` e compatível dentro do prazo | a operação segue, com lançamento e saldo no mesmo commit |
| já terminou `REJECTED` ou `FAILED` | `REFERENCE_UNSUCCESSFUL`, na hora |
| não fecha em jogador, carteira, rodada ou tipo | `REFERENCE_MISMATCH` |
| não apareceu até o prazo | `REFERENCE_NOT_FOUND` |
| existe, mas não concluiu até o prazo | `REFERENCE_NOT_PROCESSED` |

O prazo é `REFERENCE_TTL`, 15 minutos por padrão, com backoff exponencial de 1 s a 60 s entre tentativas. Uma espera encerrada é terminal.

### Consultar

```bash
curl -s http://localhost:8090/wagering/transactions/<transactionId> \
  -H "Authorization: Bearer $PROVIDER"
curl -s http://localhost:8090/providers/provider-a/wagering/transactions/bet-$WALLET_ID \
  -H "Authorization: Bearer $PROVIDER"
```

As duas devolvem o resultado gravado, com `failureCode` quando `REJECTED` e sem `balance` enquanto `PENDING_REFERENCE`. A leitura não reabre nem antecipa uma espera.

### Extrato e reconciliação

```bash
curl -s "http://localhost:8090/wallets/$WALLET_ID/ledger?limit=50" \
  -H "Authorization: Bearer $INTERNAL"
curl -s -X POST "http://localhost:8090/wallets/$WALLET_ID/reconciliation" \
  -H "Authorization: Bearer $INTERNAL"
```

O extrato sai em ordem de sequência, cada lançamento com o saldo anterior e o posterior. `limit` vai de 1 a 200 (padrão 50); quando há próxima página, `nextCursor` é devolvido em `cursor`.

```json
{
  "walletId": "…",
  "storedBalance": {"amount":"975.00","currency":"BRL"},
  "calculatedBalance": {"amount":"975.00","currency":"BRL"},
  "difference": {"amount":"0.00","currency":"BRL"},
  "version": 2, "checkedEntries": 2, "lastSequence": 2,
  "consistent": true
}
```

A reconciliação lê os dois saldos na mesma sentença SQL e não corrige nada. Quando não fecham, `divergences` lista `BALANCE_MISMATCH`, `SEQUENCE_GAP` ou `CHAIN_BREAK` — este com `firstBreakSequence`. É `POST` porque é o verbo do enunciado, mas não grava nem trava.

### Pela fila

A mesma operação pode chegar por `wager-transactions.fifo`. O envelope leva `messageId` e a operação em `data`, com a chave em `data.idempotencyKey`; o grupo é o id da carteira em minúsculas e a deduplicação é o `messageId`.

```bash
MESSAGE_ID=$(uuidgen | tr 'A-Z' 'a-z')

AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1 \
aws --endpoint-url http://localhost:4566 sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "$WALLET_ID" \
  --message-deduplication-id "$MESSAGE_ID" \
  --message-body "$(cat <<JSON
{"messageId":"$MESSAGE_ID","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",
 "data":{"providerId":"provider-a","externalTransactionId":"win-$WALLET_ID",
  "idempotencyKey":"provider-a:win-$WALLET_ID","playerId":"$PLAYER_ID","walletId":"$WALLET_ID",
  "roundId":"round-1","gameId":"crash","kind":"WIN",
  "money":{"amount":"40.00","currency":"BRL"}}}
JSON
)"
```

O consumidor confere o remetente contra o [mapa de remetentes](deploy/local/queue-senders.yaml); no LocalStack, a identidade observada é a conta local, que pode enviar por `provider-a` e `provider-b`. Desfecho commitado — inclusive `REJECTED` e `PENDING_REFERENCE` — apaga a mensagem; falha transitória a devolve com backoff. Vão para a DLQ, sem efeito financeiro: corpo ilegível, remetente ou provedor fora do mapa, `messageId` repetido com outro corpo e a quinta entrega sem sucesso.

## Eventos

Publicados no tópico SNS FIFO `wallet-events.fifo`, com a carteira como grupo e o `eventId` como deduplicação:

| Evento | Quando |
| --- | --- |
| `WagerTransactionProcessed` | a transação terminou `PROCESSED`, inclusive `LOSS` e abertura com saldo positivo |
| `WagerTransactionRejected` | a transação terminou `REJECTED` |
| `WalletBalanceChanged` | o saldo mudou |
| `WagerTransactionPendingReference` | a espera por referência foi gravada |

<details>
<summary>Envelope de exemplo</summary>

```json
{
  "eventId": "019974a4-0000-7000-8000-00000000e001",
  "eventType": "WalletBalanceChanged",
  "version": 1,
  "aggregateId": "019974a4-0000-7000-8000-00000000a11e",
  "correlationId": "01a0d779-f2df-7a49-9bb1-ac7e4e0c185f",
  "occurredAt": "2026-09-24T12:00:00Z",
  "data": {
    "walletId": "019974a4-0000-7000-8000-00000000a11e",
    "transactionId": "019974a4-0000-7000-8000-0000000000c1",
    "direction": "DEBIT",
    "money": {"amount": "25.00", "currency": "BRL"},
    "balanceBefore": {"amount": "1000.00", "currency": "BRL"},
    "balanceAfter": {"amount": "975.00", "currency": "BRL"},
    "walletVersion": 2
  }
}
```

`causationId` é o `messageId` da mensagem que causou o commit e sai omitido quando nenhuma mensagem o causou ([ADR 0030](docs/adr/0030-causation-id-e-a-mensagem-que-causou-o-commit.md)).

</details>

Por carteira, o relay publica sempre o evento pendente mais antigo, sob lease, e várias carteiras em paralelo. Falha transitória tenta de novo com backoff; dez recusas permanentes do broker marcam a linha como morta e liberam a carteira. Replay e conflitos de idempotência não emitem evento.

## Observabilidade

- **Painel "Liquidação"** no Grafana (`localhost:3000`): divergência de reconciliação no topo, e faixas de saúde e latência, resultado financeiro, fila e referência pendente, outbox e reconciliação. Os pontos do p99 abrem o trace no Tempo.
- **Três alertas** em `localhost:9095/alerts`: divergência de reconciliação, outbox com pendente há mais de 30 s, e reconciliação que o observador não conseguiu produzir.
- **Logs JSON** com `correlationId`, `trace_id` e identificadores — nunca quantia, saldo, token ou corpo.
- **Métricas** em `/metrics` de cada réplica, com exemplar de `trace_id` na latência.

Painel e regras são arquivos do repositório (`deploy/grafana`, `deploy/prometheus`), carregados na subida. Detalhes em [05 · Transversais](docs/05-transversais.md).

## Testes

A suíte de unidade não pede Docker:

```bash
go test ./...
go test -race ./...
go vet ./...
```

| Comando | O que roda | Pede a stack |
| --- | --- | --- |
| `make test` | suíte de unidade, com `-race` | não |
| `make test-journey` | suíte de jornada (`-tags=integration`) contra PostgreSQL, Keycloak e LocalStack reais | sim |
| `make scenarios` | os oito cenários de concorrência do enunciado | sim |
| `make rules-test` | teste das regras de alerta, com o `promtool` | só Docker |
| `make load` | carga de 60 s sobre as réplicas, com veredito por carteira | sobe sozinho |

As suítes com a stack pedem o schema aplicado também no banco próprio delas, `junglegaming_test`, que `make migrate` cria e migra:

```bash
make up && make migrate
make test-journey
make scenarios
```

A suíte usa banco próprio porque os workers da aplicação de pé varreriam as linhas dos casos, e roda com `-p 1` porque um dos casos encurta a vida do token no realm compartilhado.

### Cenários de concorrência

Cada cenário sobe várias instâncias independentes do processo — cada uma com o próprio grafo do Fx, pool e porta — sobre o mesmo banco e o mesmo broker ([ADR 0027](docs/adr/0027-instancias-como-grafos-do-fx-no-binario-de-teste.md)). Isso cobre o item 4 do enunciado, três ou mais instâncias, em todos os outros.

| # | Cenário | Teste |
| --- | --- | --- |
| 1 | a mesma aposta 50 vezes em paralelo → um débito | `TestSameBet_debitsOnceWhenItArrivesManyTimesAtOnce` |
| 2 | duas apostas de 80 numa carteira de 100 → uma `PROCESSED`, uma `INSUFFICIENT_FUNDS` | `TestRacingBets_settleAgainstTheBalanceAlreadyCommitted` |
| 3 | carteiras distintas em paralelo, com uma travada fora da aplicação | `TestLockedWallet_doesNotHoldTheOthers` |
| 5 | a instância morre entre o commit e a remoção da mensagem → reentrega sem segundo efeito | `TestInterruption_changesNothingWhenTheRemovalNeverReachedTheBroker` |
| 6 | dois publicadores disputando a outbox → cada evento sai uma vez | `TestPublishers_sendEachEventOnce` |
| 7 | `REFUND` e `ROLLBACK` antes da citada → resolvem quando ela chega, ou expiram | `TestEarlyReversal_waitsAndThenResolvesOrExpires` |
| 8 | reinício da frota → replay, conflito, espera concluída, eventos e ledger consistentes | `TestRestart_keepsIdempotencyTheWaitAndTheLedger` |
| — | HTTP e fila compartilham a idempotência → um efeito nas duas ordens | `TestChannels_settleTheSameOperationOnceOverHTTPAndTheQueue` |

`make scenarios` roda os oito em sequência, continua depois de uma falha e sai diferente de zero se algum falhou; o log de cada um fica em `.quality/scenarios/<teste>.log`. Um cenário isolado:

```bash
go test -race -count=1 -tags=integration -run '^TestRacingBets_settleAgainstTheBalanceAlreadyCommitted$' ./internal/e2e/scenarios/
```

<details>
<summary>Parâmetros dos cenários</summary>

Cada quantidade é uma variável de ambiente; ausente, vale o padrão do enunciado. Um valor inválido — inclusive um sob o qual o cenário não teria como falhar — falha nomeando a variável, antes de subir qualquer instância. O desfecho esperado é calculado a partir dos parâmetros: `SCENARIO_RACING_BETS=5 SCENARIO_RACING_AMOUNT=30.00` espera três `PROCESSED`, duas `INSUFFICIENT_FUNDS` e saldo `10.00`.

| Variável | Padrão | Aceita |
| --- | --- | --- |
| `SCENARIO_INSTANCES` | `3` | inteiro ≥ 2 |
| `SCENARIO_SAME_BET_COPIES` | `50` | inteiro ≥ 2 e ≥ `SCENARIO_INSTANCES` |
| `SCENARIO_OPENING_BALANCE` | `100.00` | quantia > 0 |
| `SCENARIO_RACING_BETS` | `2` | inteiro ≥ 2 |
| `SCENARIO_RACING_AMOUNT` | `80.00` | quantia que o saldo inicial comporta ao menos uma vez e menos vezes que as apostas |
| `SCENARIO_OTHER_WALLETS` | `10` | inteiro ≥ 1 |
| `SCENARIO_PUBLISHERS` | `2` | inteiro ≥ 2 |
| `SCENARIO_RESTARTS` | `1` | inteiro ≥ 1 |
| `SCENARIO_DEADLINE` | `2m` | duração > 0 |

```bash
make scenarios SCENARIO_INSTANCES=5 SCENARIO_SAME_BET_COPIES=200
make scenarios SCENARIO_REPEAT=3
```

</details>

### Integração contínua

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) valida painel, balanceador e regras de alerta; roda `golangci-lint` em cada módulo, a suíte de unidade com `-race` e os pisos de cobertura — 90% no domínio, 80% em `internal/app`, 70% em `internal/platform`; sobe a stack para a suíte de integração; e termina com carga sobre as réplicas do Compose, com uma delas morta à força, e sobre o cluster.

## Operação

| Comando | O que faz |
| --- | --- |
| `make up` / `make down` | sobe a stack e espera cada serviço ficar saudável / derruba e descarta os volumes |
| `make up REPLICAS=5` | outro número de réplicas; o balanceador enxerga até 10 |
| `make cluster-up` / `make cluster-down` | as réplicas num cluster Kind sobre os serviços do Compose, em `localhost:8091` / de volta ao Compose |
| `make load` | carga com veredito; `TARGET=cluster`, `REPLICAS`, `LOAD_KILL=graceful\|forced` |
| `make verify` | confere o ambiente contra os arquivos versionados: bancos e versão do schema, realm, filas e tópico, imagem, painel e alertas |
| `make migrate` | aplica o schema nos dois bancos, o da aplicação e o da suíte |
| `make mutation` / `make cover-journey` | teste de mutação do módulo / cobertura somando unidade e integração |
| `make provision` | refaz o apply do broker no LocalStack |
| `make help` | lista todos os alvos |

Os dois modos de réplica, o relatório e os números medidos da carga, e os exercícios de falha estão em [07 · Operação](docs/07-operacao.md).

**Migrations.** O SQL versionado fica em [`deploy/migrations`](deploy/migrations) e é aplicado pelo `golang-migrate`, na subida ou com `make migrate`; aplicar de novo não altera nada. Reverter é o `down` da migration, ou `make down` para recriar do zero:

```bash
docker compose run --rm migrate -path=/migrations \
  -database "postgres://junglegaming:junglegaming@postgres:5432/junglegaming?sslmode=disable" down 1
```

`make migrate-reversibility` sobe, reverte e sobe de novo o banco da suíte, e confere que a versão não ficou suja e que o papel da aplicação continua escrevendo.

<details>
<summary>Os comandos por trás dos alvos</summary>

Cada alvo é um atalho para comandos que também rodam à mão, com os valores de `.env.example`:

```bash
# make up
WAGER_REPLICAS=3 docker compose up -d --build --wait

# make provision — o estado e a chave do remetente IAM ficam em deploy/terraform/localstack/
docker compose run --rm provision

# make migrate — cada banco nomeado no próprio comando
docker compose run --rm migrate -path=/migrations \
  -database "postgres://junglegaming:junglegaming@postgres:5432/junglegaming?sslmode=disable" up
docker compose exec -T postgres psql -U junglegaming -d junglegaming -tAc \
  "SELECT 1 FROM pg_database WHERE datname='junglegaming_test'" | grep -q 1 \
  || docker compose exec -T postgres createdb -U junglegaming junglegaming_test
docker compose run --rm migrate -path=/migrations \
  -database "postgres://junglegaming:junglegaming@postgres:5432/junglegaming_test?sslmode=disable" up

# make test — scripts/ tem go.mod próprio, e o ./... da raiz para na fronteira de módulo
go test -race -count=1 ./...
for m in envcheck testgates loadtest; do go test -C scripts/$m -race -count=1 ./...; done

# make test-journey — em série e no banco da suíte
DATABASE_URL="postgres://junglegaming:junglegaming@localhost:5432/junglegaming_test?sslmode=disable" \
  go test -race -count=1 -p 1 -tags=integration ./...

# make verify
go run -C scripts/envcheck . -root "$PWD"

# make down
docker compose down -v
```

</details>

**Problemas comuns**

- **`localhost:8090` responde `503`** — nenhuma réplica está pronta. `docker compose ps` mostra o estado de cada uma; com o cluster de pé, as réplicas do Compose ficam paradas e a API está em `localhost:8091`.
- **Filas ou tópico sumiram** — o LocalStack não persiste estado. Um reinício pelo Compose reprovisiona sozinho; um reinício por fora dele (`docker restart`, o daemon voltando) pede `make provision`.
- **Algo não bate e não se sabe o quê** — `make verify` nomeia o que divergiu, e funciona mesmo quando a aplicação não sobe.

## Configuração

Tudo por variável de ambiente, validada antes de qualquer porta abrir: ausente ou malformada impede a subida. O Compose já define as obrigatórias; para rodar o binário no host, [.env.example](.env.example) traz cada uma. A lista completa, por grupo, está em [01 · Contexto](docs/01-contexto.md#configuração).

<details>
<summary>Trabalho de fundo e seus padrões</summary>

| Variável | Padrão | O que controla |
| --- | --- | --- |
| `REFERENCE_TTL` | `15m` | quanto uma operação espera pela que cita |
| `REFERENCE_INTERVAL` | `1s` | varredura do worker de referência |
| `OUTBOX_INTERVAL` | `1s` | varredura do relay |
| `OUTBOX_LEASE` | `30s` | quanto uma reivindicação segura a linha da outbox |
| `QUEUE_POLL` | `20s` | espera do long poll |
| `QUEUE_VISIBILITY` | `30s` | invisibilidade da mensagem; tem de cobrir `QUEUE_POLL` + `QUEUE_TIMEOUT` |
| `QUEUE_TIMEOUT` | `8s` | prazo de decisão de uma mensagem |
| `RECONCILIATION_INTERVAL` | `5s` | intervalo entre páginas do observador de divergência |
| `RECONCILIATION_BATCH` | `50` | carteiras por página |
| `SHUTDOWN_TIMEOUT` | `20s` | prazo do encerramento no `SIGTERM` |

</details>

## Documentação

| Quero saber | Onde |
| --- | --- |
| a visão de cima: estilo, camadas, invariantes, pastas | [ARCHITECTURE.md](ARCHITECTURE.md) |
| quem fala com o sistema, do que ele é feito, o que configura | [01 · Contexto e containers](docs/01-contexto.md) |
| agregados, tipos de operação, ciclo de vida | [02 · Domínio](docs/02-dominio.md) |
| tabelas, invariantes no banco, migrations | [03 · Dados](docs/03-dados.md) |
| aposta, replay, corrida e espera, passo a passo | [04 · Fluxos](docs/04-fluxos.md) |
| erros, autorização, observabilidade, ciclo de vida do processo | [05 · Transversais](docs/05-transversais.md) |
| o que falta, o que é guarda, o que custa | [06 · Riscos e limitações](docs/06-riscos-e-limitacoes.md) |
| réplicas, carga e exercícios de falha | [07 · Operação](docs/07-operacao.md) |
| por que foi feito assim, e o que foi rejeitado | [Decisões (ADRs)](docs/adr/README.md) |

## Estrutura

```
cmd/wager/            o binário
internal/domain/      regras: money, identity, wallet, ledger, wager, event
internal/app/         casos de uso, um por pacote, e as portas que eles declaram
internal/platform/    adaptadores: HTTP, PostgreSQL, SQS e SNS, Keycloak, telemetria, Fx
internal/e2e/         suíte de jornada e cenários de concorrência
deploy/               migrations, Terraform, Keycloak, HAProxy, Kubernetes, observabilidade
scripts/              gates do CI, verificador do ambiente, teste de carga
docs/                 estrutura (0X) e decisões (adr/)
```
