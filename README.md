# Ambiente local

Base compartilhada da liquidação: PostgreSQL, LocalStack, Keycloak, o cano de telemetria e o processo `wager`.

As decisões de desenho — dinheiro, máquina de estados, idempotência, lock, reversões, inbox e outbox, autorização, Fx e encerramento — estão em [ARCHITECTURE.md](ARCHITECTURE.md), com o estado de implementação de cada uma.

## Pré-requisitos

- Docker com Compose v2
- Terraform 1.5 ou mais novo
- As portas `5432`, `4566`, `8080`, `8090`, `4317`, `4318` e `3000` livres no host

## Subida

Na raiz do repositório, sem precisar de um `.env`:

```bash
docker compose up --build
```

Os defaults do Compose são os valores de `.env.example`. São senhas locais do desafio, não credencial de produção.

A subida aplica o schema antes de o processo escutar: o serviço `migrate` roda uma vez, com o `wager` esperando `service_completed_successfully`. O binário da aplicação não carrega código de migration.

| Serviço | Endereço |
| --- | --- |
| PostgreSQL | `localhost:5432`, database `junglegaming`, usuário e senha `junglegaming` |
| LocalStack | `localhost:4566` |
| Keycloak | `localhost:8080` |
| Coletor OTLP | `localhost:4317` (gRPC) e `localhost:4318` (HTTP) |
| Grafana | `localhost:3000`, usuário e senha `admin` |
| Processo `wager` | `localhost:8090` |

Consulta dos backends, a partir do host: Tempo em `localhost:3200`, Loki em `localhost:3100`, Prometheus em `localhost:9095`.

## Schema

O SQL versionado fica em `deploy/migrations`, com arquivos numerados aplicados pelo `golang-migrate`. Aplicar de novo o mesmo conjunto termina com sucesso e não altera o schema.

```bash
docker compose run --rm migrate
```

Reverter em desenvolvimento é o `down` da migration, ou `docker compose down -v` para recriar do zero.

As três tabelas financeiras são `wallets`, `wager_transactions` e `ledger_entries`, e ao lado delas fica a `outbox_events`, com o `eventId` como chave, o payload imutável depois da inserção e o índice parcial que serve a varredura da fila de publicação. As invariantes que o agregado não substitui ficam no banco: saldo não negativo, unicidade de jogador mais moeda, os campos exigidos por tipo e por status, uma reversão `PROCESSED` por transação citada, e o ledger recusando `UPDATE`, `DELETE` e `TRUNCATE`.

A migration cria o papel `wager_app`, que tem apenas `SELECT` e `INSERT` no ledger, e concede esse papel a quem conectou. A aplicação entra com `SET ROLE` em cada conexão do pool, então o privilégio vale mesmo quando quem conecta é superusuário. Sem o schema aplicado, `GET /health/ready` responde 503 e `GET /health/live` continua 200.

## Rotas de carteira

`POST /wallets` abre a carteira e `GET /wallets/:walletId` devolve o estado gravado. As duas exigem token do cliente interno.

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/realms/junglegaming/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=wallet-internal \
  -d client_secret=wallet-internal-local | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')

curl -s -X POST http://localhost:8090/wallets \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"playerId":"3f8c4a2e-1b5d-4e7a-9c3f-2d6b8a1e5c40","initialBalance":{"amount":"1000.00","currency":"BRL"}}'
```

Saldo inicial positivo grava carteira, transação `OPENING` já `PROCESSED` e o lançamento de crédito no mesmo commit. Saldo inicial zero grava só a carteira. A carteira nasce na versão 1, e dinheiro entra e sai como `{"amount":"1000.00","currency":"BRL"}` — string decimal de duas casas, nunca número JSON.

A segunda carteira do mesmo jogador na mesma moeda responde 409, decidido pela unicidade do banco e não por consulta prévia. Carteira inexistente na URL responde 404. Entrada inválida responde 400 sem gravar linha. Todo corpo de erro é `application/problem+json` conforme a RFC 9457, e `failureCode` aparece em extensão só quando a recusa é de regra de negócio.

## Rotas de aposta

`POST /wagering/transactions` liquida a operação do provedor e `GET /wagering/transactions/{transactionId}` devolve o resultado gravado. As duas exigem token de provedor: o cliente interno recebe 403 nelas, e o provedor continua recebendo 403 nas rotas de carteira.

A rota aceita `BET`, `LOSS`, `WIN`, `REFUND` e `ROLLBACK`. O corpo leva `referenceExternalTransactionId` quando a operação cita outra: as duas reversões sempre o exigem, e um `WIN` pode trazê-lo. O campo presente e fora de formato responde 400 sem gravar linha; ausente, a operação se decide sozinha.

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/realms/junglegaming/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=provider-a \
  -d client_secret=provider-a-local | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')

curl -i -X POST http://localhost:8090/wagering/transactions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: key-0001' \
  -d '{"providerId":"provider-a","externalTransactionId":"ext-0001",
       "playerId":"3f8c4a2e-1b5d-4e7a-9c3f-2d6b8a1e5c40","walletId":"<id da carteira>",
       "roundId":"round-0001","gameId":"crash","kind":"BET",
       "money":{"amount":"25.00","currency":"BRL"}}'
```

O `providerId` do corpo não autoriza: vale o cliente do token, e um corpo declarando outro provedor responde 403 sem gravar linha. A chave de idempotência vem no cabeçalho `Idempotency-Key`; sem ela a resposta é 400 e nada é gravado.

A primeira conclusão responde 201, com `Location` apontando o recurso criado e o saldo observado no commit. `BET` debita e grava o lançamento no mesmo commit da transação; `WIN` sem operação citada credita; `LOSS` termina `PROCESSED` com quantia zero, sem lançamento e sem mudar a versão da carteira.

A mesma chave com o mesmo corpo responde 200 com `idempotentReplay: true` e o saldo observado na conclusão original — não o saldo atual. A mesma chave com outro corpo responde 422 `IDEMPOTENCY_CONFLICT`, e o mesmo `externalTransactionId` com outra chave responde 422 `DUPLICATE_EXTERNAL_TRANSACTION`. Nenhuma das duas grava segunda linha: quem decide a duplicidade é o índice único do banco, não uma consulta prévia que duas réplicas vencem ao mesmo tempo.

`INSUFFICIENT_FUNDS`, `PLAYER_WALLET_MISMATCH` e `CURRENCY_MISMATCH` gravam a transação `REJECTED` com o token, sem lançamento e sem mexer no saldo, e o mesmo vale para os tokens decididos sobre a carteira travada: `REVERSAL_INSUFFICIENT_FUNDS`, `REVERSAL_AMOUNT_MISMATCH`, `REFERENCE_MISMATCH`, `REFERENCE_UNSUCCESSFUL` e `ALREADY_REVERSED`. **A rejeição durável ocupa a chave de idempotência**: reenviar a mesma chave com o mesmo corpo devolve a mesma recusa, agora marcada como replay, e tentar de novo de verdade exige chave nova. `WALLET_NOT_FOUND`, `OPENING_NOT_ALLOWED`, `AMOUNT_NOT_ALLOWED_FOR_KIND` e `REFERENCE_REQUIRED` recusam sem gravar linha, porque a linha correspondente violaria as invariantes da tabela.

Duas apostas simultâneas na mesma carteira se serializam pelo lock da linha: a segunda lê o saldo já commitado e, se não couber, sai com `INSUFFICIENT_FUNDS`. Carteiras diferentes não esperam uma pela outra.

### A operação que cita outra

`REFUND` estorna um `BET` e `ROLLBACK` reverte um `WIN` ou um `REFUND`, sempre pelo valor integral da citada — valor diferente responde 422 `REVERSAL_AMOUNT_MISMATCH`. Cada operação aceita uma reversão `PROCESSED` só: a segunda responde 422 `ALREADY_REVERSED`, com a linha gravada e sem lançamento novo.

Quando a operação citada já está `PROCESSED`, tudo se decide no mesmo commit da submissão — como no `ext-0001` acima, que um `REFUND` de `"25.00"` citando-o estorna na hora. Quando ela ainda não chegou pelo outro canal — aqui `ext-0009`, que ninguém enviou —, a submissão é **aceita e fica esperando**:

```bash
curl -i -X POST http://localhost:8090/wagering/transactions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: key-0002' \
  -d '{"providerId":"provider-a","externalTransactionId":"ext-0002",
       "playerId":"3f8c4a2e-1b5d-4e7a-9c3f-2d6b8a1e5c40","walletId":"<id da carteira>",
       "roundId":"round-0009","gameId":"crash","kind":"WIN",
       "referenceExternalTransactionId":"ext-0009",
       "money":{"amount":"50.00","currency":"BRL"}}'
```

A resposta é **202**, com `Location` apontando o recurso criado e o estado `PENDING_REFERENCE`. Ela não traz saldo observado, porque nenhum commit concluiu a operação, e não é problem details: nenhuma regra a recusou. O código é próprio de propósito — 201 com o estado no corpo obrigaria o provedor a ler o corpo para saber se houve movimento financeiro.

O worker de referência é o processo de fundo que encerra essa espera. Ele sobe com o binário, varre a fila no intervalo configurado e decide cada linha sob o lock da carteira:

- a citada chega `PROCESSED` e compatível dentro do prazo: a operação segue, com o lançamento e o saldo no mesmo commit;
- a citada já terminou `REJECTED` ou `FAILED`: `REFERENCE_UNSUCCESSFUL` na hora, sem aguardar o prazo;
- a citada chega e não fecha em jogador, carteira, rodada ou tipo: `REFERENCE_MISMATCH`;
- o prazo vence e a citada não existe: `REFERENCE_NOT_FOUND`;
- o prazo vence e a citada existe sem ter concluído: `REFERENCE_NOT_PROCESSED`.

O prazo é gravado uma vez, na entrada, como `agora + REFERENCE_TTL` — 15 minutos por padrão. Entre tentativas o worker aplica backoff exponencial de base 1s, fator 2 e teto de 60s, com o intervalo sorteado e nunca agendado depois do prazo. Uma espera encerrada é terminal: a citada chegando depois não reabre nada.

Enquanto a espera dura, a mesma chave com o mesmo corpo responde 200 com `PENDING_REFERENCE` e `idempotentReplay: true`, sem saldo observado — o reenvio não antecipa o prazo nem mexe no agendamento. Depois do encerramento, ela devolve o desfecho gravado.

A consulta devolve o resultado gravado ao provedor dono:

```bash
curl -s http://localhost:8090/wagering/transactions/<transactionId> \
  -H "Authorization: Bearer $TOKEN"
```

Ela responde 200 com estado, quantia, saldo observado quando houver e `failureCode` quando o estado é `REJECTED` — a leitura concluiu, então não é problem details. Uma transação em `PENDING_REFERENCE` responde 200 com esse estado e sem saldo observado; a leitura não tenta a espera, não antecipa o prazo e não mexe no agendamento. Transação de outro provedor responde 404 igual a uma inexistente: nem o corpo nem o status revelam que o registro existe.

## Broker

Com o LocalStack saudável:

```bash
terraform -chdir=deploy/terraform/localstack init
terraform -chdir=deploy/terraform/localstack apply
```

O estado fica em `deploy/terraform/localstack/terraform.tfstate`. Não há Terraform Cloud.

O apply cria:

- fila FIFO `wager-transactions.fifo`, long poll de 20s, visibility de 30s, redrive com `maxReceiveCount` 15
- DLQ FIFO `wager-transactions-dlq.fifo`
- tópico SNS FIFO `wallet-events.fifo`, sem subscription — não há consumidor dos eventos ainda, e é para ele que o relay publica
- usuário IAM `wager-sender`, com `sqs:SendMessage` só na fila de entrada

A access key gerada fica em `deploy/terraform/localstack/wager-sender.keys`, fora do Git. O mapa versionado de remetente está em `deploy/local/queue-senders.yaml`, e ele **não** nomeia o principal IAM: esse nome não chega ao consumidor, então um mapa por ele não poderia ser conferido contra mensagem nenhuma. Cada entrada é chaveada pela identidade que o consumidor observa na mensagem, com a própria lista de provedores; no broker local essa identidade é o identificador da conta, e a entrada dela pode enviar por `provider-a` e `provider-b`.

O mapa admite mais de uma entrada, ainda que o apply crie um remetente só. É assim que a configuração de produção nomeia vários remetentes, e é o que permite a suíte de jornada exercitar a recusa escolhendo a credencial de envio — o broker local deriva da access key a identidade que registra.

No LocalStack community o IAM é parcial: a chave do `wager-sender` consegue `SendMessage`, mas a política pode não ser aplicada como na AWS. O principal continua criado.

O LocalStack community não persiste: qualquer reinício do container esvazia filas e tópico, com ou sem `docker compose down -v`. O `terraform.tfstate` no disco continua afirmando que eles existem, e é o refresh do próximo `terraform apply` que percebe a diferença e recria tudo. Depois de reiniciar o LocalStack, rode o apply de novo. A access key do `wager-sender` muda nessa recriação, e `wager-sender.keys` é reescrito.

O ready do processo só fica verde depois desse apply: a fila `wager-transactions.fifo` precisa existir, e sem ela `GET /health/ready` responde 503 enquanto `GET /health/live` continua 200. O tópico não entra no ready, mas precisa existir para o relay publicar: sem ele a linha da outbox fica na fila e o log do processo mostra a recusa. O `pprof` escuta em `127.0.0.1:6060` dentro do container e o Compose não publica essa porta.

No encerramento, o Compose dá 30s ao processo antes do `SIGKILL`, acima dos 10s de `SHUTDOWN_TIMEOUT`. A margem existe para o buffer de telemetria descarregar depois que o servidor fecha. No `SIGTERM` os três componentes de fundo param de reivindicar ou buscar. O worker de referência conclui ou desfaz a espera que já reivindicou dentro do mesmo prazo — desfeita, a linha continua `PENDING_REFERENCE` com o prazo intacto, disponível para outra réplica. O relay conclui o envio em curso dentro do lease que já segura — o sinal corta a varredura, não o envio que já tem reivindicação, e quem corta o envio é o prazo do processo. O que ele não confirmar continua publicável, com o mesmo `eventId`, e outra réplica reivindica depois do prazo. O consumidor da fila para de buscar mensagem nova e conclui a que tem em mãos dentro do prazo; cortada por ele, a mensagem volta à fila com visibilidade zero, para ser entregue de novo sem esperar a invisibilidade que ninguém mais vai servir.

Sete variáveis configuram o trabalho de fundo, e as sete têm padrão: `REFERENCE_TTL` é quanto uma operação espera pela que ela cita, 15 minutos; `REFERENCE_INTERVAL` é de quanto em quanto tempo o worker varre a fila de esperas, 1 segundo; `OUTBOX_INTERVAL` é de quanto em quanto tempo o relay varre a outbox, 1 segundo; `OUTBOX_LEASE` é quanto uma reivindicação segura a linha, 30 segundos; `QUEUE_POLL` é a espera do long poll, 20 segundos; `QUEUE_VISIBILITY` é a invisibilidade da mensagem, 30 segundos; `QUEUE_TIMEOUT` é o prazo de uma decisão, 8 segundos. Qualquer uma delas escrita com algo que não seja uma duração positiva impede a subida em vez de cair no padrão.

As três últimas não são três números independentes: a invisibilidade tem de cobrir a espera do long poll somada ao prazo da decisão. Com invisibilidade menor que a espera, medido no broker local, a busca devolve resposta vazia **e consome a entrega** — a mensagem queima o orçamento de entregas sem nunca ter sido processada, sem erro e sem log. Os padrões são os da fila que o apply provisiona: 20 de espera sob 30 de invisibilidade, com o prazo da decisão abaixo dos 10 que sobram.

`SNS_ENDPOINT` e `SNS_TOPIC_ARN` não têm padrão: sem o endereço do tópico o processo não abre a porta HTTP e não sobe o relay. `SQS_QUEUE_URL`, `SQS_DLQ_URL` e `QUEUE_SENDERS_PATH` também não: sem a fila de entrada o consumidor não tem de onde buscar, sem a DLQ ele não teria como abandonar uma mensagem envenenada — que ficaria na frente da carteira dela para sempre —, e sem o mapa de remetente o processo não sabe quem pode enviar. No Compose eles apontam para o LocalStack e para `arn:aws:sns:us-east-1:000000000000:wallet-events.fifo`.

## Eventos

A liquidação publica quatro eventos, e só estes:

| Evento | Quando |
| --- | --- |
| `WagerTransactionProcessed` | a transação terminou `PROCESSED`, inclusive `LOSS` e a abertura de carteira com saldo positivo |
| `WagerTransactionRejected` | a transação terminou `REJECTED`, com o `failureCode` gravado |
| `WalletBalanceChanged` | o saldo da carteira mudou |
| `WagerTransactionPendingReference` | a espera pela operação citada foi gravada |

`FAILED` não tem evento próprio. `LOSS` e a abertura com saldo zero não emitem `WalletBalanceChanged`, porque nenhum dos dois produz lançamento. Replay e os dois conflitos de idempotência não emitem nada, porque nenhum deles grava transação.

Todos saem no mesmo envelope, com dinheiro em string decimal de duas casas, igual ao contrato de entrada. O `aggregateId` é a carteira, que é o que ordena a publicação:

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
    "money": { "amount": "25.00", "currency": "BRL" },
    "balanceBefore": { "amount": "1000.00", "currency": "BRL" },
    "balanceAfter": { "amount": "975.00", "currency": "BRL" },
    "walletVersion": 2
  }
}
```

`causationId` é o `messageId` da mensagem que causou o commit, e sai omitido em todo commit que nenhuma mensagem causou: por HTTP a operação não tem mensagem que a cause, e o commit que o worker de referência conclui por prazo vencido é disparado pelo relógio. Emprestar ali o identificador da mensagem que abriu a espera diria que ela causou um evento que ela não causou; quem liga os dois commits é o identificador da transação, que o primeiro evento carrega e o segundo usa como correlação. O construtor fixa o tipo e a `version`; nenhum chamador escolhe os dois.

### Como o evento sai

A linha do evento entra na mesma transação SQL que grava saldo, transação e lançamento — saldo e evento vivem ou morrem juntos, e uma operação desfeita não deixa evento nenhum. Nada é publicado antes do commit.

O relay é o segundo componente de fundo do binário. A cada `OUTBOX_INTERVAL` ele varre a outbox e, por carteira, pega o evento não publicado mais antigo; reivindica a linha com um token novo e um lease de `OUTBOX_LEASE`, publica no tópico com a carteira como grupo e o `eventId` como deduplicação, e confirma só se o token ainda for o da reivindicação. A linha que outra réplica segura é pulada, não esperada, então uma carteira travada não para a fila das outras.

Falha transitória do broker devolve a linha para nova tentativa, com backoff de 1s, fator 2 e teto de 60s, e conta como tentativa. Recusa permanente conta à parte, e repetida dez vezes marca a linha como morta e solta a carteira, para os eventos seguintes dela seguirem; a linha permanece no banco, com o mesmo `eventId` e o mesmo payload.

A entrega é ao menos uma vez. A deduplicação do broker pelo `eventId` cobre a janela de cinco minutos do FIFO, e o `eventId` estável cobre o resto — do lado de quem consome.

O envio aparece no log com identificadores apenas: `eventId`, `walletId`, o tipo do evento, o desfecho, e o `trace_id` e o `span_id` do commit que gravou a linha.

```bash
docker compose logs -f wager | grep "outbox event"
```

O tópico é provisionado sem subscription, então não há de onde ler as mensagens fora da suíte de jornada, que anexa um assinante só pelo tempo do caso. Para olhar à mão, crie uma fila FIFO e assine com `RawMessageDelivery`.

## Aposta pela fila

O consumidor da fila é o terceiro componente de fundo do binário. Ele busca em long poll, decide cada mensagem pelo mesmo caso de uso da rota HTTP, e responde ao broker: apaga a mensagem cujo desfecho commitou, devolve com backoff a que falhou de forma transitória, e copia para a DLQ a que nenhuma repetição resolveria.

O envelope leva o `messageId` e, opcionalmente, o `correlationId`; a operação vai em `data`, com o mesmo corpo da rota HTTP mais a chave de idempotência em `data.idempotencyKey`. O grupo da mensagem é o id da carteira em minúsculas, e a deduplicação é o `messageId` do envelope.

```bash
# a access key do apply está em deploy/terraform/localstack/wager-sender.keys
BODY=$(cat <<JSON
{"messageId":"$MESSAGE_ID","data":{
  "providerId":"provider-a","externalTransactionId":"external-1",
  "idempotencyKey":"key-1","playerId":"$PLAYER_ID","walletId":"$WALLET_ID",
  "roundId":"round-1","gameId":"game-1","kind":"BET",
  "money":{"amount":"25.00","currency":"BRL"}}}
JSON
)

aws --endpoint-url http://localhost:4566 sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "$WALLET_ID" \
  --message-deduplication-id "$MESSAGE_ID" \
  --message-body "$BODY"
```

Rejeição de negócio é desfecho concluído: a transação fica `REJECTED` com o `failureCode` e a mensagem sai da fila, porque reentregar não mudaria a decisão. A espera gravada é tratada do mesmo modo — a linha é durável e o worker de referência a conclui, então a mensagem de entrada não fica na fila esperando por isso.

Quatro razões levam uma mensagem à DLQ, e nenhuma delas grava linha financeira: corpo que a borda não leu, remetente que o mapa não nomeia, corpo declarando provedor fora da lista daquele remetente, e o `messageId` já gravado chegando com outro corpo. Some-se a desistência na quinta entrega, abaixo do `maxReceiveCount` 15 da fila: o grupo da mensagem é a carteira, então uma mensagem envenenada segura as operações daquela carteira até sair do caminho, e é por isso que o consumidor a tira antes do broker.

Toda ida para a DLQ sai no log com a razão e o `messageId`, e a recusa por remetente sai com a identidade observada — é esse valor que corrige o mapa. As mesmas razões contam na série `wager_ingress_messages_abandoned_total`, e a profundidade da DLQ sai em `wager_ingress_dead_letter_depth`.

```bash
docker compose logs -f wager | grep "dead-letter"
```

## Token

O issuer é `http://localhost:8080/realms/junglegaming`. Os três clientes usam `client_credentials`. O mapa, sem segredo, está em `deploy/local/clients.yaml`: `wallet-internal` no papel interno de carteira, `provider-a` e `provider-b` nos `providerId` de mesmo nome.

O processo recebe esse mapa em `CLIENTS_PATH` e o issuer em `IDP_ISSUER`. Sem um dos dois, ou com um mapa ilegível, a subida termina com erro e a porta HTTP não abre: um processo sem mapa não sabe autorizar ninguém. `IDP_JWKS_URL` é opcional e existe porque o endereço muda de lado: o issuer é o que o token anuncia em `iss`, visto do host, e o JWKS é buscado de dentro da rede do Compose. Vazio, ele é derivado do issuer.

Quem autoriza é o cliente do token, não o corpo nem a URL. Nas rotas de carteira, só o papel interno passa: o provedor recebe 403 sem criar nada, e um cliente válido fora do mapa também. Credencial ausente, inválida ou expirada é a outra classe, 401. `/health/live` e `/health/ready` seguem públicas.

```bash
curl -s -X POST http://localhost:8080/realms/junglegaming/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=wallet-internal \
  -d client_secret=wallet-internal-local
```

Troque `client_id` e `client_secret` por `provider-a` / `provider-a-local` ou `provider-b` / `provider-b-local`. O grant de senha está desligado nos três.

## Telemetria

O coletor recebe OTLP, aplica batch e entrega trace ao Tempo, log ao Loki e métrica ao Prometheus. O Grafana só provisiona esses três datasources. Não há dashboard de negócio nem regra de alerta.

O processo manda trace e log por OTLP. A série de processo não vai por esse caminho: ela sai pelo `/metrics`, que o Prometheus raspa em `wager:8090`, para heap e goroutines terem uma fonte só. O outro alvo, `otel-collector:8889`, publica o que chegar ao coletor por OTLP.

A latência HTTP sai em OpenMetrics com exemplar de `trace_id`. O Prometheus sobe com `--enable-feature=exemplar-storage` e o datasource liga esse exemplar ao Tempo, então o ponto do gráfico abre o trace.

`/health/live`, `/health/ready` e `/metrics` ficam fora do span e do log. São chamados de segundo em segundo pela sonda e pelo scrape, e afogariam o trace e o histograma que o dashboard vai usar.

## Testes

A suíte de unidade não sobe Docker:

```bash
go test ./...
go test -race ./...
go vet ./...
```

A suíte de jornada vive em `internal/e2e/`, atrás da tag `integration`, e pede o ambiente de pé, o schema aplicado e o broker provisionado:

```bash
docker compose up -d --wait postgres localstack keycloak otel-collector
docker compose run --rm migrate
terraform -chdir=deploy/terraform/localstack apply -auto-approve
go test -race -tags=integration ./...
```

A suíte pede IdP real: ela obtém token dos três clientes e, no caso do token expirado, encurta o `accessTokenLifespan` do realm pela API de administração e o restaura no fim. Trocar o Keycloak por um emissor de teste não provaria a borda.

A suíte cai nos valores do LocalStack — banco, endpoint, coletor e credencial — quando eles não vêm do ambiente, e `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` e `AWS_REGION` sobrescrevem isso para apontar em outro broker. Os três estão em `.env.example`. O código de produção não carrega credencial fixa: o cliente SQS usa a cadeia padrão do SDK, que no Compose e no CI lê o ambiente e na nuvem leria o papel.

Além do `go test`, o gate do CI roda `scripts/testgates` — estrutura dos testes e piso de cobertura, 90% em `money`, `wallet`, `wager`, `ledger` e `identity`, 80% em `internal/app` e 70% em `internal/platform` — e o `.golangci.yml`.
