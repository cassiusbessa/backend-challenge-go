# Ambiente local

Base compartilhada da liquidação: PostgreSQL, LocalStack, Keycloak e o cano de telemetria. O processo da aplicação não entra neste Compose.

## Pré-requisitos

- Docker com Compose v2
- Terraform 1.5 ou mais novo
- As portas `5432`, `4566`, `8080`, `4317`, `4318` e `3000` livres no host

## Subida

Na raiz do repositório, sem precisar de um `.env`:

```bash
docker compose up --build
```

Os defaults do Compose são os valores de `.env.example`. São senhas locais do desafio, não credencial de produção.

| Serviço | Endereço |
| --- | --- |
| PostgreSQL | `localhost:5432`, database `junglegaming`, usuário e senha `junglegaming` |
| LocalStack | `localhost:4566` |
| Keycloak | `localhost:8080` |
| Coletor OTLP | `localhost:4317` (gRPC) e `localhost:4318` (HTTP) |
| Grafana | `localhost:3000`, usuário e senha `admin` |

Consulta dos backends, a partir do host: Tempo em `localhost:3200`, Loki em `localhost:3100`, Prometheus em `localhost:9095`.

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

`docker compose down -v` apaga o volume do LocalStack. Rode `terraform apply` de novo depois disso. O estado no disco deixa de bater com o broker vazio.

## Token

O issuer é `http://localhost:8080/realms/junglegaming`. Os três clientes usam `client_credentials`. O mapa, sem segredo, está em `deploy/local/clients.yaml`: `wallet-internal` no papel interno de carteira, `provider-a` e `provider-b` nos `providerId` de mesmo nome.

```bash
curl -s -X POST http://localhost:8080/realms/junglegaming/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=wallet-internal \
  -d client_secret=wallet-internal-local
```

Troque `client_id` e `client_secret` por `provider-a` / `provider-a-local` ou `provider-b` / `provider-b-local`. O grant de senha está desligado nos três.

## Telemetria

O coletor recebe OTLP, aplica batch e entrega trace ao Tempo, log ao Loki e métrica ao Prometheus. O Grafana só provisiona esses três datasources. Não há dashboard de negócio nem regra de alerta.
