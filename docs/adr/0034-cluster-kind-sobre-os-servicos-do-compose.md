# 0034. O cluster Kind sobre os serviços do Compose

Status: aceita · 2026-09-26

## Contexto

As réplicas do Compose ([0033](0033-replicas-do-compose-atras-de-um-haproxy.md)) já são processos separados, mas o caminho de operação pedido é Kubernetes: N réplicas da mesma imagem, a migration como Job que roda uma vez antes delas, as sondas de saúde, e a parada coberta pelo prazo. PostgreSQL, LocalStack, Keycloak, o coletor e o Prometheus já sobem no Compose, com o realm, o apply do broker e o banco da suíte. Falta decidir onde esses serviços ficam, qual cluster local, como a migration se ordena antes das réplicas e de onde vem o número delas.

## Opções consideradas

1. k3d, com os serviços do Compose.
2. Kind, com PostgreSQL, LocalStack, Keycloak e a telemetria reescritos como manifestos dentro do cluster.
3. Kind, com o nó ligado à rede do Compose, os serviços onde já estão, a migration num initContainer de cada réplica e o número escrito no manifesto.
4. Kind, com o nó ligado à rede do Compose, os serviços onde já estão, a migration como Job ordenado pelo `make`, e o número como parâmetro do comando.

## Decisão

Opção 4. O k3d traz Traefik e um balanceador próprio que precisariam ser desligados, e o repasse do DNS do Docker aos pods muda de padrão entre versões; o Kind foi medido antes: com o nó ligado à rede `junglegaming`, um pod resolve `postgres`, `localstack`, `keycloak`, `otel-collector` e `prometheus` pelo nome, e o token pedido de dentro do pod sai com o mesmo `iss` que o processo já aceita. Reescrever os serviços no cluster duplicaria o realm, o Terraform, a configuração do coletor e o banco da suíte num segundo formato, e o que o cluster prova — processos separados sobre os mesmos serviços — não ganharia nada com isso.

O initContainer migraria a cada reinício de cada réplica, o contrário de "uma vez", e um initContainer que só esperasse o Job pediria `kubectl` na imagem e RBAC para esperar; a ordem já é do comando, que apaga o Job anterior, aplica o novo, espera `Complete` ou `Failed` e só então aplica o Deployment. O número escrito no manifesto voltaria a cada `apply` antes de um `scale` corrigir; sem ele, o `kubectl scale` depois do `apply` é quem manda, e o `apply` seguinte não o desfaz.

## Consequências

- `make cluster-up` sobe tudo com um comando, sem Terraform no host: parte do mesmo `docker compose up`, para as réplicas do Compose, cria o cluster se não existe, liga o nó à rede, carrega as imagens, gera o Secret e os ConfigMaps das mesmas variáveis e arquivos que o Compose lê, e roda o Job antes do Deployment.
- As réplicas atendem em `localhost:8091`, por NodePort; `8090` continua sendo o balanceador do Compose, que fica sem destino enquanto o cluster existe.
- Na terminação, o `preStop` de 5 s tira a réplica do Service antes do `SIGTERM`, e os 35 s de prazo cobrem esse atraso, o `SHUTDOWN_TIMEOUT` de 20 s e os 10 s que o Compose já deixa além dele, onde cabem os 3 s de descarga da telemetria.
- As imagens entram no nó por `docker save --platform` e `kind load image-archive`: com o image store do containerd, o `kind load docker-image` de uma imagem baixada exporta o índice de todas as plataformas e o import falha.
- **O que se paga:** o nó é ligado à rede por fora da configuração do Kind, então `make down` apaga o cluster antes de o Compose remover a rede. Um `docker compose up` à mão com o cluster de pé devolve as réplicas do Compose em silêncio, e elas disputam a outbox e a fila com os pods; o caminho de volta é `make cluster-down`. O host precisa de `kubectl`, e o Kind compila por `go run` na primeira vez.

## Onde está no código

- `deploy/k8s/kind.yaml` — o cluster de um nó e o mapeamento de `30091` para `localhost:8091`.
- `deploy/k8s/migrate.yaml` — o Job com `backoffLimit: 0`, as migrations montadas e o banco vindo do Secret.
- `deploy/k8s/wager.yaml` — o Deployment sem `replicas`, as sondas, o `preStop` e o prazo, e o Service NodePort.
- `Makefile` — `cluster-up`, `cluster-down`, `MIGRATE_JOB_WAIT`, `CHECK_REPLICAS` e o `down` que apaga o cluster antes da rede.
