# Ambiente local

Base compartilhada da liquidação: PostgreSQL, LocalStack, Keycloak, o cano de telemetria e o processo `wager`.

A arquitetura está em [ARCHITECTURE.md](ARCHITECTURE.md): a visão de cima — estilo, padrões, estrutura de pastas, invariantes — com links para a estrutura detalhada em `docs/` e para cada decisão de desenho, com a alternativa que rejeitou, em `docs/adr/`.

## Pré-requisitos

- Docker com Compose v2
- Terraform 1.5 ou mais novo
- As portas `5432`, `4566`, `8080`, `8090`, `4317`, `4318` e `3000` livres no host
- `make` e Go 1.27.1, para o runner e o verificador

## Atalhos e verificação

Cada ritual deste documento tem um alvo no `Makefile`, e cada alvo é o comando
que está publicado aqui — o atalho não esconde nada. `make help` lista todos.

```bash
make up          # docker compose up -d --build --wait
make provision   # o apply do Terraform contra o LocalStack
make migrate     # o schema nos dois bancos, cada um nomeado no comando
make test        # a suíte de unidade
make test-journey  # a suíte de jornada, em série e no banco dela
make scenarios   # os oito cenários de concorrência do enunciado, um go test cada
make rules-test  # o teste de unidade das duas regras de alerta, com o promtool da imagem
make verify      # o ambiente está no estado que os arquivos versionados declaram?
make down        # derruba a stack e descarta os volumes dela
```

`make verify` é o único que não aparece em outra seção. Ele não altera nada e
responde por seis coisas: os dois bancos existem, estão na mesma versão de
schema e nessa versão que o `*.up.sql` mais alto de `deploy/migrations` declara —
aplicada a nenhum dos dois, uma migration os deixaria concordando e atrasados; o
realm emite token pelo tempo que `deploy/keycloak/junglegaming-realm.json`
declara; as filas e o tópico que o Terraform descreve existem no broker; e a
imagem em execução não é mais antiga que o último commit que mudou o que ela
contém — os caminhos que o `Dockerfile` copia, menos os `_test.go`, que só o
estágio de build enxerga; o Grafana tem o painel "Liquidação" provisionado; e o
Prometheus carregou cada regra que `deploy/prometheus/rules/settlement.yml`
declara, pelo nome. Ele sai
diferente de zero nomeando o que divergiu, e funciona quando a aplicação não sobe
— que é quando ele é chamado.

```bash
go run -C scripts/envcheck . -root "$PWD"
```

Os defaults de flag do verificador são os valores deste repositório, e é por isso
que o comando acima roda sem argumento. Quem tem um `.env` que troca o banco ou a
senha do administrador usa `make verify`, que passa cada um deles explicitamente:
o make inclui o `.env` pelo mesmo motivo que o Compose o lê.

Dois alvos existem para medir, e nenhum dos dois entra na subida: `make
cover-journey`, que soma o perfil das duas suítes com `go tool covdata`, e `make
mutation`, que roda o gremlins sobre o módulo. E `make migrate-reversibility`
sobe, reverte e sobe de novo **o banco da suíte**, conferindo que a versão não
ficou suja e que os dois bancos continuam aceitando escrita pelo papel da
aplicação — o revertido porque são os privilégios dele que a reversão revoga e a
subida reconcede, e o da aplicação porque o papel é objeto de cluster e os dois o
compartilham; ele derruba schema, e é por isso que o nome diz contra quem roda.

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

Reverter em desenvolvimento é o `down` da migration, ou `docker compose down -v` para recriar do zero. A reversão revoga os privilégios que concedeu e deixa de pé o papel `wager_app`, que é objeto de cluster compartilhado pelos dois bancos — derrubá-lo ao reverter um deles falharia enquanto o outro existisse. Quem quiser conferir a reversibilidade usa `make migrate-reversibility`, que derruba schema só no banco da suíte e nunca no da aplicação, e prova a escrita pelo papel nos dois.

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

## Leituras da carteira

`GET /wallets/{walletId}/ledger` devolve o extrato paginado e `GET /wallets/{walletId}/reconciliation` compara o saldo gravado com o que o ledger soma. As duas exigem o mesmo token do cliente interno das rotas de carteira; o provedor recebe 403 sem lançamento nem saldo no corpo. Nenhuma das duas grava linha, move saldo ou toma lock.

```bash
curl -s "http://localhost:8090/wallets/<id da carteira>/ledger?limit=2" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{
  "walletId": "<id da carteira>",
  "entries": [
    {"id": "…", "transactionId": "…", "direction": "CREDIT", "sequenceNumber": 1,
     "amount": {"amount":"1000.00","currency":"BRL"},
     "balanceBefore": {"amount":"0.00","currency":"BRL"},
     "balanceAfter": {"amount":"1000.00","currency":"BRL"},
     "createdAt": "2026-09-26T12:00:00Z"},
    {"id": "…", "transactionId": "…", "direction": "DEBIT", "sequenceNumber": 2, "…": "…"}
  ],
  "nextCursor": "MTExMTExMTEt…"
}
```

Os lançamentos saem em ordem de sequência, cada um com o saldo anterior e o posterior, e `limit` é o tamanho da página — 50 por padrão, no máximo 200; `nextCursor` é um token opaco que só aparece quando há página seguinte, e é passado de volta em `cursor` para continuar exatamente do lançamento seguinte ao último devolvido, mesmo que outro tenha sido gravado no meio. `limit` fora da faixa ou não inteiro, cursor que a rota não emitiu e cursor emitido para outra carteira respondem 400 nomeando o campo, sem consultar o ledger — e os dois últimos com o mesmo corpo, para a recusa não dizer nada sobre a outra carteira.

```bash
curl -s "http://localhost:8090/wallets/<id da carteira>/reconciliation" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{
  "walletId": "<id da carteira>",
  "storedBalance": {"amount":"1100.00","currency":"BRL"},
  "ledgerBalance": {"amount":"1000.00","currency":"BRL"},
  "version": 1, "entryCount": 1, "lastSequence": 1,
  "consistent": false,
  "divergences": ["BALANCE_MISMATCH"]
}
```

Os dois saldos saem da mesma sentença SQL, então um commit entre as leituras não inventa desvio, e a leitura não espera uma aposta que esteja com a carteira travada. `consistent` é verdadeiro quando o ledger fecha com o saldo; senão `divergences` lista o que desviou, de um vocabulário fechado: `BALANCE_MISMATCH` quando a soma difere do saldo gravado, `SEQUENCE_GAP` quando a contagem de lançamentos difere da última sequência, e `CHAIN_BREAK` quando um lançamento não começa onde o anterior terminou — este com `firstBreakSequence` apontando o primeiro. Uma carteira consistente omite os dois campos. A rota informa e não corrige; toda divergência deixa uma linha de log com o `walletId` e os tokens, sem saldo.

Ninguém precisa chamar a rota para uma divergência aparecer. O observador de divergência, o quarto componente de fundo do binário, varre todas as carteiras em páginas de `RECONCILIATION_BATCH` a cada `RECONCILIATION_INTERVAL`, e produz o mesmo veredito pela mesma sentença. Ele só lê: não toma lock, não abre transação e não corrige nada, então uma carteira divergente reaparece a cada passagem até alguém corrigir o banco. A divergência que ele encontra deixa a mesma linha de log e move `wager_reconciliation_divergences_total` com a origem `watch`, e é essa série que o alerta observa.

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

No encerramento, o Compose dá 30s ao processo antes do `SIGKILL`, acima dos 10s de `SHUTDOWN_TIMEOUT`. A margem existe para o buffer de telemetria descarregar depois que o servidor fecha. No `SIGTERM` os quatro componentes de fundo param de reivindicar, buscar ou ler. O worker de referência conclui ou desfaz a espera que já reivindicou dentro do mesmo prazo — desfeita, a linha continua `PENDING_REFERENCE` com o prazo intacto, disponível para outra réplica. O relay conclui o envio em curso dentro do lease que já segura — o sinal corta a varredura, não o envio que já tem reivindicação, e quem corta o envio é o prazo do processo. O que ele não confirmar continua publicável, com o mesmo `eventId`, e outra réplica reivindica depois do prazo. O consumidor da fila para de buscar mensagem nova e conclui a que tem em mãos dentro do prazo; cortada por ele, a mensagem volta à fila com visibilidade zero, para ser entregue de novo sem esperar a invisibilidade que ninguém mais vai servir. O observador de divergência não começa turno novo, e a leitura em curso é cancelada: nada do que ele faz é gravável, então não há o que concluir.

Nove variáveis configuram o trabalho de fundo, e as nove têm padrão: `REFERENCE_TTL` é quanto uma operação espera pela que ela cita, 15 minutos; `REFERENCE_INTERVAL` é de quanto em quanto tempo o worker varre a fila de esperas, 1 segundo; `OUTBOX_INTERVAL` é de quanto em quanto tempo o relay varre a outbox, 1 segundo; `OUTBOX_LEASE` é quanto uma reivindicação segura a linha, 30 segundos; `QUEUE_POLL` é a espera do long poll, 20 segundos; `QUEUE_VISIBILITY` é a invisibilidade da mensagem, 30 segundos; `QUEUE_TIMEOUT` é o prazo de uma decisão, 8 segundos; `RECONCILIATION_INTERVAL` é de quanto em quanto tempo o observador lê uma página de carteiras, 5 segundos; `RECONCILIATION_BATCH` é quantas carteiras cabem numa página, 50. Qualquer uma das oito durações escrita com algo que não seja uma duração positiva, ou um lote que não seja inteiro positivo, impede a subida em vez de cair no padrão. Os dois números do observador são ponto de partida: o custo de um turno é o lote vezes o ledger de cada carteira, e ainda não foi medido sob carga.

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

O coletor recebe OTLP, aplica batch e entrega trace ao Tempo, log ao Loki e métrica ao Prometheus. O Grafana provisiona esses três datasources e o painel "Liquidação"; o Prometheus carrega as duas regras de alerta. Os dois são arquivos do repositório, lidos na subida.

O painel está em `deploy/grafana/dashboards/liquidacao.json` e abre em `localhost:3000` com a senha de exemplo. No topo, as divergências de reconciliação dos últimos 15 minutos, vermelhas acima de zero; abaixo, cinco faixas na ordem: saúde e latência, com o p99 cujos pontos abrem o trace no Tempo; resultado financeiro, com desfechos, rejeições por `failureCode` e duplicatas; fila e referência pendente, com as duas profundidades, os retries e a idade da espera mais antiga; outbox, com pendentes, a idade do mais antigo, as linhas mortas e os retries; reconciliação, com carteiras conferidas e divergências por origem. O painel é provisionado: pela interface ele se explora, mas não se salva, porque o arquivo é a fonte.

As regras estão em `deploy/prometheus/rules/settlement.yml`, e a página `localhost:9095/alerts` mostra o estado das duas:

- `ReconciliationDivergenceFound` dispara quando `wager_reconciliation_divergences_total` subiu nos últimos 15 minutos, sem espera: a divergência é achado, não tendência.
- `OutboxOldestPendingTooOld` dispara quando o evento pendente mais antigo passa de 30 s por mais de um minuto. O lease de um envio é de 30 s, e o minuto é o que deixa um envio lento legítimo passar sem alerta.

Não há Alertmanager: o alerta aparece no Prometheus e no Grafana, e não há para onde notificar num ambiente local. As regras têm teste de unidade, que roda sem a stack com o `promtool` da mesma imagem do Prometheus:

```bash
make rules-test
```

O processo manda trace e log por OTLP. A série de processo não vai por esse caminho: ela sai pelo `/metrics`, que o Prometheus raspa em `wager:8090`, para heap e goroutines terem uma fonte só. O outro alvo, `otel-collector:8889`, publica o que chegar ao coletor por OTLP.

A latência HTTP sai em OpenMetrics com exemplar de `trace_id`. O Prometheus sobe com `--enable-feature=exemplar-storage` e o datasource liga esse exemplar ao Tempo, então o ponto do gráfico abre o trace.

`/health/live`, `/health/ready` e `/metrics` ficam fora do span e do log. São chamados de segundo em segundo pela sonda e pelo scrape, e afogariam o trace e o histograma que o painel usa.

## Testes

A suíte de unidade não sobe Docker:

```bash
go test ./...
go test -race ./...
go vet ./...
```

A suíte de jornada vive em `internal/e2e/`, atrás da tag `integration`, e pede o ambiente de pé, o broker provisionado e o schema aplicado nos dois bancos — o da aplicação e o dela:

```bash
docker compose up -d --wait postgres localstack keycloak otel-collector
terraform -chdir=deploy/terraform/localstack apply -auto-approve

# o banco da suíte, uma vez e idempotente: CREATE DATABASE não aceita IF NOT EXISTS
docker compose exec -T postgres psql -U junglegaming -d junglegaming -tAc \
  "SELECT 1 FROM pg_database WHERE datname='junglegaming_test'" | grep -q 1 \
  || docker compose exec -T postgres createdb -U junglegaming junglegaming_test

# cada banco nomeado no comando que o migra
docker compose run --rm migrate
docker compose run --rm migrate -path=/migrations \
  -database "postgres://junglegaming:junglegaming@postgres:5432/junglegaming_test?sslmode=disable" up

DATABASE_URL="postgres://junglegaming:junglegaming@localhost:5432/junglegaming_test?sslmode=disable" \
  go test -race -count=1 -p 1 -tags=integration ./...
```

O banco próprio não é gosto: a aplicação de pé tem os próprios workers, e o relay de outbox dela varre `outbox_events` inteira a cada segundo, sem filtrar carteira, e publica a linha que um caso espera ver morta.

O `-p 1` é a outra metade do mesmo estado compartilhado: a suíte de carteira encurta o `accessTokenLifespan` do realm para provar que a borda recusa token expirado, e qualquer pacote que peça token em paralelo dentro dessa janela recebe 401.

Migration nova precisa ser aplicada nos dois bancos. Aplicada só num, a suíte falha num `relation does not exist` em vez de dizer que o banco está atrasado.

A suíte pede IdP real: ela obtém token dos três clientes e, no caso do token expirado, encurta o `accessTokenLifespan` do realm pela API de administração e o restaura no fim. Trocar o Keycloak por um emissor de teste não provaria a borda.

A suíte cai nos próprios defaults — banco, endpoint, coletor e credencial — quando eles não vêm do ambiente. O default de `DATABASE_URL` é o banco da suíte, e não o da aplicação: o comando acima o exporta por clareza, e quem esquecer continua caindo no banco certo. `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` e `AWS_REGION` sobrescrevem a credencial para apontar em outro broker. Os três estão em `.env.example`. O código de produção não carrega credencial fixa: o cliente SQS usa a cadeia padrão do SDK, que no Compose e no CI lê o ambiente e na nuvem leria o papel.

Além do `go test`, o gate do CI roda `scripts/testgates` — estrutura dos testes e piso de cobertura, 90% em `money`, `wallet`, `wager`, `ledger` e `identity`, 80% em `internal/app` e 70% em `internal/platform` — e o `.golangci.yml`. Antes de qualquer Go ele confere que o painel é JSON válido e roda o teste das regras de alerta.

### Os cenários obrigatórios

Os testes de concorrência que o enunciado exige moram num pacote próprio, `internal/e2e/scenarios`, um teste por item, contra PostgreSQL, Keycloak e LocalStack reais. Todo cenário sobe várias instâncias independentes do processo — cada uma com o próprio grafo do Fx, pool, porta e componentes de fundo — sobre o mesmo banco e o mesmo broker, e reparte as chegadas entre elas; é assim que o item 4, três ou mais instâncias, vale para todos os outros ([ADR 0027](docs/adr/0027-instancias-como-grafos-do-fx-no-binario-de-teste.md)). A morte entre o commit e a remoção da mensagem, e a contagem de cada evento publicado, são feitas por um proxy do teste entre a instância e o broker ([ADR 0028](docs/adr/0028-falha-e-contagem-na-fronteira-de-rede-do-broker.md)).

Pedem o mesmo ambiente da suíte de jornada, e cada um roda sozinho com o próprio comando. O `DATABASE_URL` pode ficar de fora: o default já é o banco da suíte.

```bash
# 1. a mesma aposta 50 vezes em paralelo → um débito
go test -race -count=1 -tags=integration -run '^TestSameBet_debitsOnceWhenItArrivesManyTimesAtOnce$' ./internal/e2e/scenarios/
# 2. duas apostas de 80 numa carteira de 100 → uma PROCESSED, uma INSUFFICIENT_FUNDS, e o reenvio não muda nada
go test -race -count=1 -tags=integration -run '^TestRacingBets_settleAgainstTheBalanceAlreadyCommitted$' ./internal/e2e/scenarios/
# 3. carteiras distintas ao mesmo tempo, com uma delas travada fora da aplicação
go test -race -count=1 -tags=integration -run '^TestLockedWallet_doesNotHoldTheOthers$' ./internal/e2e/scenarios/
# 5. a instância morre depois do commit e antes da remoção → outra recebe a reentrega, sem segundo efeito
go test -race -count=1 -tags=integration -run '^TestInterruption_changesNothingWhenTheRemovalNeverReachedTheBroker$' ./internal/e2e/scenarios/
# 6. dois publicadores disputando a outbox → cada evento sai uma vez
go test -race -count=1 -tags=integration -run '^TestPublishers_sendEachEventOnce$' ./internal/e2e/scenarios/
# 7. REFUND e ROLLBACK antes da citada → resolvem quando ela chega, ou expiram
go test -race -count=1 -tags=integration -run '^TestEarlyReversal_waitsAndThenResolvesOrExpires$' ./internal/e2e/scenarios/
# 8. reinício da frota inteira → replay, conflito, espera concluída, eventos publicados, reconciliação consistente
go test -race -count=1 -tags=integration -run '^TestRestart_keepsIdempotencyTheWaitAndTheLedger$' ./internal/e2e/scenarios/
# HTTP e SQS compartilham a idempotência → um efeito nas duas ordens, e conflito com outro corpo
go test -race -count=1 -tags=integration -run '^TestChannels_settleTheSameOperationOnceOverHTTPAndTheQueue$' ./internal/e2e/scenarios/
```

Cada quantidade é uma variável de ambiente, e a ausente vale o padrão do enunciado. Um valor inválido falha o cenário nomeando a variável, antes de subir qualquer instância, e nunca cai no padrão. O desfecho esperado é calculado a partir dos parâmetros: `SCENARIO_RACING_BETS=5 SCENARIO_RACING_AMOUNT=30.00` espera três `PROCESSED`, duas `INSUFFICIENT_FUNDS` e saldo 10.00.

Inválido é também o valor sob o qual um cenário não teria como falhar. Com uma instância só não há outra para receber a chegada, com um publicador não há disputa, com uma cópia não há replay e com uma aposta não há corrida; por isso o piso dessas quatro é dois. E duas combinações são recusadas mesmo com cada valor aceito sozinho: menos cópias da mesma aposta do que instâncias, que deixa uma instância sem chegada, e uma quantia de corrida que o saldo inicial não comporta nenhuma vez ou comporta uma vez por aposta — nos dois casos nenhuma aposta disputa o saldo, e o desfecho seria o mesmo sem o lock.

| Variável | Padrão | Origem | Aceita |
| --- | --- | --- | --- |
| `SCENARIO_INSTANCES` | `3` | enunciado | inteiro ≥ 2 |
| `SCENARIO_SAME_BET_COPIES` | `50` | enunciado | inteiro ≥ 2 e ≥ `SCENARIO_INSTANCES` |
| `SCENARIO_OPENING_BALANCE` | `100.00` | enunciado | quantia > 0 |
| `SCENARIO_RACING_BETS` | `2` | enunciado | inteiro ≥ 2 |
| `SCENARIO_RACING_AMOUNT` | `80.00` | enunciado | quantia > 0 que o saldo inicial comporta ao menos uma vez e menos vezes que as apostas |
| `SCENARIO_OTHER_WALLETS` | `10` | estes cenários — o enunciado não fixa | inteiro ≥ 1 |
| `SCENARIO_PUBLISHERS` | `2` | enunciado | inteiro ≥ 2 |
| `SCENARIO_RESTARTS` | `1` | estes cenários — o enunciado não fixa | inteiro ≥ 1 |
| `SCENARIO_DEADLINE` | `2m` | estes cenários: o prazo de cada caso | duração > 0 |

`make scenarios` roda os oito em sequência, cada um no próprio `go test`, contra o banco da suíte. Ele continua depois de um cenário que falhou, termina listando os que falharam e sai diferente de zero se algum falhou. Os parâmetros passam pela linha de comando ou pelo ambiente, e `SCENARIO_REPEAT` repete cada cenário — cada execução monta os próprios dados, então repetir contra a mesma stack é legítimo:

```bash
make scenarios
make scenarios SCENARIO_INSTANCES=5 SCENARIO_SAME_BET_COPIES=200
make scenarios SCENARIO_REPEAT=3
```

A tela mostra, por cenário, o que ele mediu e o veredito — as instâncias que subiram, quantas chegadas cada uma decidiu, os desfechos contados, o saldo e a versão, quanto tempo cada espera levou —, e numa falha a linha que diz o que o caso esperava. O log inteiro de cada um, com o JSON de todas as instâncias, fica em `.quality/scenarios/<nome>.log`, e o resumo das falhas aponta para ele:

```
== TestSameBet_debitsOnceWhenItArrivesManyTimesAtOnce
    samebet_test.go:29: 3 instances up: http://127.0.0.1:42231 http://127.0.0.1:43879 http://127.0.0.1:43425
    samebet_test.go:39: sent 50 copies of one bet under one key at once
    samebet_test.go:41: 1 settled as 01a0dee7-9a3d-… with 975.00 observed, 49 answered the replay of it
    samebet_test.go:42: wallet: 975.00 at version 2, with one transaction under the key and one debit
    samebet_test.go:43: arrivals each instance decided, by its own series: [17 17 16], 50 in all
--- PASS: TestSameBet_debitsOnceWhenItArrivesManyTimesAtOnce (0.34s)
```

O passo de integração do CI roda o pacote com os padrões, dentro do `./...`. Medido na stack local, numa máquina de 16 CPUs, o pacote leva cerca de 14 s; metade disso é o cenário da interrupção, que espera os 6 s de invisibilidade separarem as duas entregas.
