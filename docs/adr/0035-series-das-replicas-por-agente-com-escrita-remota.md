# 0035. As séries das réplicas do cluster por um agente com escrita remota

Status: aceita · 2026-09-26

## Contexto

O painel "Liquidação" e as regras de alerta consultam o Prometheus do Compose, que descobre as réplicas do Compose pelo DNS do Docker e raspa cada uma. As réplicas do cluster ([0034](0034-cluster-kind-sobre-os-servicos-do-compose.md)) rodam atrás de um NodePort, com IP de pod que só existe dentro do nó. As séries de cada uma precisam chegar ao mesmo backend, distinguíveis por réplica, com o exemplar de `trace_id` da latência, sem que o painel e as regras mudem.

## Opções consideradas

1. O Prometheus do Compose raspa os pods direto.
2. O Prometheus do Compose raspa os pods pelo proxy do API server do cluster.
3. Um StatefulSet com um NodePort por réplica, raspado do Compose.
4. Um Prometheus completo dentro do cluster, com o painel apontando para os dois.
5. Um Prometheus em modo agente dentro do cluster, que descobre os pods e escreve no Prometheus do Compose por escrita remota.

## Decisão

Opção 5. O IP de pod não é roteável de fora do nó, então a opção 1 não alcança nada. O proxy do API server poria no `prometheus.yml` do Compose a credencial de um cluster que nem sempre existe, e um alvo caído em toda subida sem cluster. Um NodePort por réplica trocaria o tipo do workload só para servir ao scrape. Um Prometheus completo no cluster obrigaria o painel e as regras a consultar dois lugares, e o alerta que só olha um deixaria de ver metade das réplicas.

O agente é a mesma imagem do Compose com `--agent`: descobre os pods `app=wager` com uma Role que só lê pods do namespace, raspa a cada 5 s, rotula cada série pelo pod, e manda tudo, exemplares inclusive, para o Prometheus do Compose, que passa a aceitar escrita remota.

## Consequências

- O painel e as regras veem as réplicas do cluster sem edição, porque nenhuma consulta filtra por `job`; o p99 continua abrindo o trace, porque o exemplar viaja na escrita.
- Uma réplica substituída aparece como outra série, com outro `pod`, em vez de sobrescrever a anterior.
- Sem cluster, nada escreve, e a rota de escrita remota do Prometheus do Compose fica sem uso.
- **O que se paga:** a série do cluster passa por dois saltos — o scrape do agente e a escrita remota —, e quem lê o fim de uma janela espera os dois; a carga espera 15 s. As séries do agente também carregam `instance`, então quem separa os dois alvos filtra por `pod` e não por `instance`.

## Onde está no código

- `deploy/k8s/metrics-agent.yaml` — ServiceAccount, Role, RoleBinding, a configuração do agente e o Deployment.
- `compose.yaml` — `--web.enable-remote-write-receiver` no serviço `prometheus`.
- `scripts/loadtest/main.go` — o seletor de cada alvo, `pod=""` para o Compose e `pod!=""` para o cluster.
