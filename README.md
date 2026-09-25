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

As três tabelas financeiras são `wallets`, `wager_transactions` e `ledger_entries`. As invariantes que o agregado não substitui ficam no banco: saldo não negativo, unicidade de jogador mais moeda, os campos exigidos por tipo e por status, uma reversão `PROCESSED` por transação citada, e o ledger recusando `UPDATE`, `DELETE` e `TRUNCATE`.

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
- tópico SNS FIFO `wallet-events.fifo`, sem subscription
- usuário IAM `wager-sender`, com `sqs:SendMessage` só na fila de entrada

A access key gerada fica em `deploy/terraform/localstack/wager-sender.keys`, fora do Git. O principal versionado está em `deploy/local/queue-senders.yaml` e pode enviar por `provider-a` e `provider-b`.

No LocalStack community o IAM é parcial: a chave do `wager-sender` consegue `SendMessage`, mas a política pode não ser aplicada como na AWS. O principal continua criado.

O LocalStack community não persiste: qualquer reinício do container esvazia filas e tópico, com ou sem `docker compose down -v`. O `terraform.tfstate` no disco continua afirmando que eles existem, e é o refresh do próximo `terraform apply` que percebe a diferença e recria tudo. Depois de reiniciar o LocalStack, rode o apply de novo. A access key do `wager-sender` muda nessa recriação, e `wager-sender.keys` é reescrito.

O ready do processo só fica verde depois desse apply: a fila `wager-transactions.fifo` precisa existir. Sem ela, `GET /health/ready` responde 503 e `GET /health/live` continua 200. O `pprof` escuta em `127.0.0.1:6060` dentro do container e o Compose não publica essa porta.

No encerramento, o Compose dá 30s ao processo antes do `SIGKILL`, acima dos 10s de `SHUTDOWN_TIMEOUT`. A margem existe para o buffer de telemetria descarregar depois que o servidor fecha. No `SIGTERM` o worker de referência para de reivindicar espera nova, e a que ele já reivindicou conclui ou desfaz dentro do mesmo prazo — desfeita, a linha continua `PENDING_REFERENCE` com o prazo intacto, disponível para outra réplica.

Duas variáveis configuram a espera por referência, e as duas têm padrão: `REFERENCE_TTL` é quanto uma operação espera pela que ela cita, 15 minutos; `REFERENCE_INTERVAL` é de quanto em quanto tempo o worker varre a fila, 1 segundo. Qualquer uma delas escrita com algo que não seja uma duração positiva impede a subida em vez de cair no padrão.

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
