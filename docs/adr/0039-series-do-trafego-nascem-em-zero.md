# 0039. As séries do tráfego nascem em zero

Status: aceita · 2026-09-27

## Contexto

`rate()` e `increase()` leem a primeira amostra de uma série como linha de base, não como subida. Um filho de contador só passava a existir no primeiro incremento e chegava ao primeiro scrape com o que já tinha contado: trinta apostas numa stack recém-subida criaram, em cada uma das três réplicas, uma série que nasceu em 10, e os painéis de desfecho e de rejeição ficaram em zero. O [0024](0024-observador-de-divergencia-por-cursor-em-memoria.md) já tinha resolvido isso só para as séries de reconciliação, porque o alerta dependia delas; as séries do tráfego e a latência HTTP continuavam perdendo a primeira rajada de cada combinação de rótulos, por réplica.

## Opções consideradas

1. Deixar como está: a perda é só da primeira rajada de cada combinação, e o tráfego contínuo a esconde.
2. Ligar no Prometheus a ingestão do zero sintético pelo timestamp de criação do contador (`created-timestamp-zero-ingestion`).
3. Criar em zero, no construtor, cada combinação de rótulos que o código move, a partir de tabelas fechadas.

## Decisão

Opção 3. A opção 1 falha justamente na demonstração: stack nova, alguns pedidos, painel plano. A opção 2 resolveria todo contador de uma vez, sem tabela nenhuma, mas é uma flag experimental que depende de o formato do scrape trazer o timestamp de criação; teria de ser ligada no Prometheus do Compose e no agente do cluster, mexeria no scrape de que dependem os exemplares, e a correção do painel passaria a morar numa configuração que nenhum teste de unidade exercita. As tabelas ficam no pacote que já fixa todo valor de rótulo, e o teste as confere.

A tabela é exata onde um painel agrupa: origem e status das liquidações, componente e razão dos retries, origem e razão das duplicatas. O worker só fecha espera de tipo que cita, e só a fila distingue reentrega de replay. As rejeições cobrem todo o catálogo menos os dois conflitos de idempotência, que vão para as duplicatas: o catálogo é fechado para que a série por token seja enumerável.

## Consequências

- A primeira operação de cada combinação, em cada réplica, já é uma subida para `rate()` e `increase()`, e os painéis de desfecho, rejeição, duplicata, retry, DLQ, requisições por status e p99 mostram a primeira rajada.
- Os motivos de ida para a DLQ passam a ser constantes do pacote de métricas, como as outras razões; o consumidor da fila os usa de lá.
- Um teste não pode mais dizer que "nada se moveu" contando os filhos de um vetor: ele soma os valores.
- **O que se paga:** as tabelas repetem o que os pontos de chamada movem. Uma combinação nova que não entrar na tabela volta a perder a primeira subida, sem erro nenhum. E cada réplica expõe cerca de cem séries de negócio e 250 de latência em zero, a maioria parada.

## Onde está no código

- `internal/platform/metrics/metrics.go` — `prime`, `settledAs`, `duplicatedBy`, `retriedBy`, `abandonReasons` e as constantes `Abandon*`.
- `internal/platform/httpapi/metrics.go` — `answered` e o laço em `NewMetrics`.
- `internal/platform/metrics/metricstest/metricstest.go` — `Sum`, a leitura dos testes que antes contavam filhos.
