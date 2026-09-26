# 0025. Alertas como regras do Prometheus, testadas com o promtool da imagem

Status: aceita · 2026-09-26

## Contexto

Dois alertas precisam existir como arquivo versionado: divergência de reconciliação acima de zero, e evento da outbox esperando mais de 30 segundos. O ambiente local sobe Prometheus e Grafana, os dois capazes de avaliar uma regra, e não há canal para onde notificar. Um alerta que ninguém testou é descoberto quebrado no dia em que deveria ter disparado.

## Opções consideradas

1. Alerting provisionado do Grafana, em arquivo de provisionamento.
2. Regras do Prometheus, com um Alertmanager no Compose para rotear.
3. Regras do Prometheus sem Alertmanager, com teste de unidade no formato do `promtool`, rodado pelo CI com o `promtool` da mesma imagem que o Compose sobe.

## Decisão

Opção 3. O alerting do Grafana não tem teste de unidade, o arquivo de provisionamento é várias vezes mais verboso que a regra, e a avaliação passa a depender do Grafana estar de pé — o Prometheus já está sempre, e o Grafana lista as regras dele pelo datasource. O Alertmanager seria um serviço a mais sem destino: a regra pede alerta versionado, não entrega.

O `promtool` vem da imagem `prom/prometheus` do Compose, então a versão que testa é a versão que avalia; o CI já roda Docker, e não entra ferramenta nova.

As duas expressões carregam uma escolha cada. A divergência não tem `for`: é um achado, não uma tendência. A outbox tem `for: 1m`, porque o lease de um envio é de 30 s e uma linha reivindicada por um envio lento é legítima por esse tempo — quem absorve o lease é a janela do alerta, e não um filtro no gauge, que continua medindo o atraso que o consumidor sente.

## Consequências

- O teste cobre a divergência que dispara e a série estável que não dispara; a outbox acima de 30 s por um minuto que dispara, acima por menos de um minuto que não dispara, e 29 s que não dispara. Uma expressão trocada deixa o CI vermelho.
- `make verify` confere na stack de pé que o Prometheus carregou as duas regras pelo nome, lidas do mesmo arquivo que ele monta.
- **O que se paga:** o alerta fica visível no Prometheus e no Grafana, e ninguém é avisado; notificar é trabalho de quem levar isto a um ambiente com canal. E o alerta de divergência não se apaga sozinho: enquanto a carteira continuar quebrada, cada passagem do observador o mantém de pé.

## Onde está no código

- `deploy/prometheus/rules/settlement.yml` — `ReconciliationDivergenceFound`, `OutboxOldestPendingTooOld`.
- `deploy/prometheus/rules/settlement_test.yml` — os cinco casos.
- `deploy/prometheus/prometheus.yml` — `rule_files`.
- `Makefile` — o alvo `rules-test`; `.github/workflows/ci.yml` — o passo `alert rules`.
- `scripts/envcheck/main.go` — `checkRules`; `scripts/envcheck/check.go` — `declaredAlerts`, `missingRules`.
