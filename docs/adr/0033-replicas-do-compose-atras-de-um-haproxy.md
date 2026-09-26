# 0033. As réplicas do Compose atrás de um HAProxy

Status: aceita · 2026-09-26

## Contexto

O enunciado pede cenários com três ou mais instâncias e comandos de multi-instância. Os cenários obrigatórios sobem as instâncias como grafos do Fx no binário de teste ([0027](0027-instancias-como-grafos-do-fx-no-binario-de-teste.md)), e o caminho planejado para réplicas como processos separados é um cluster Kind, que pede `kubectl` e um cluster. O `docker compose up --build`, o comando reproduzível do enunciado, subia uma réplica só, publicada em `localhost:8090`. Várias réplicas não dividem a porta do host, e alguém tem de decidir para qual vai cada pedido e tirar da rotação a que não está pronta.

## Opções consideradas

1. Uma porta do host por réplica, de `8090` a `8092`.
2. nginx na frente, com o nome do serviço como upstream.
3. Traefik, descobrindo as réplicas pelos rótulos do Docker.
4. HAProxy na frente, descobrindo as réplicas pelo DNS do Docker e sondando a readiness de cada uma.

## Decisão

Opção 4. Com a opção 1, cada cliente escolhe uma réplica: os exemplos do README deixam de valer para todas, e nenhuma sai da rotação quando perde o banco ou a fila. O nginx aberto não tem sonda ativa e só descobre a réplica caída quando um pedido falha nela. O Traefik lê os rótulos pelo socket do Docker montado no contêiner, privilégio demais para balancear três processos.

O HAProxy faz a sonda de readiness que o Service do Kubernetes faria — é ela que tira a réplica do caminho quando recebe `SIGTERM` ou perde o banco — e reenvia a outra réplica a conexão recusada, sem reenviar o pedido que já chegou a uma: o `POST /wallets` não leva chave de idempotência, e repetido responderia 409 a quem pediu uma carteira só. O Prometheus raspa cada réplica pela mesma descoberta de DNS, que também dispensa o socket.

O Kind continua sendo o caminho de operação. Estas réplicas são a alternativa para quem só tem Docker, e o comando do enunciado já as sobe, três por padrão.

## Consequências

- `localhost:8090` continua sendo o endereço das rotas. `WAGER_REPLICAS` muda o número para o Compose, e `REPLICAS` para o `make up`, que recusa um valor inválido antes de tocar a stack.
- Medido sob carga contínua: `docker stop` numa réplica não fez pedido falhar — a conexão recusada foi para outra, e a réplica saiu da rotação em até dois segundos; `docker kill` fez falhar só os pedidos em curso nela; com o PostgreSQL pausado, o balanceador responde 503 sem encaminhar e nenhuma réplica reinicia.
- O PostgreSQL do Compose aceita 300 conexões: três pools, as instâncias de um cenário e a suíte de jornada passavam do padrão de 100.
- **O que se paga:** o `server-template` enxerga até dez réplicas, e as excedentes não recebem tráfego. O rótulo `instance` do Prometheus é um IP, então uma réplica recriada aparece como outra. O log de três processos sai intercalado, e a memória é três vezes a de um. E o pedido que já chegou a uma réplica que morre falha para o cliente, em vez de ir para outra.

## Onde está no código

- `deploy/haproxy/haproxy.cfg` — `resolvers docker`, o `server-template` e a sonda de `/health/ready`.
- `compose.yaml` — o serviço `balancer`, o `deploy.replicas` do `wager` e o `max_connections` do `postgres`.
- `deploy/prometheus/prometheus.yml` — o job `wager` com `dns_sd_configs`.
- `Makefile` — a validação de `REPLICAS` no alvo `up`.
