# Operação

Como rodar N réplicas do processo, como carregá-las e o que se vê quando uma dependência falha. Tudo aqui foi executado sobre a stack deste repositório, e cada exercício diz só o que se observou ao executá-lo.

## Os dois jeitos de rodar N réplicas

As réplicas são a mesma imagem, cada uma com o próprio processo, memória e pool, sobre o mesmo PostgreSQL, o mesmo LocalStack e o mesmo Keycloak. Nenhuma garantia depende da memória de uma delas: o lock da carteira, os índices de idempotência e o lease da outbox moram no banco.

| | Compose | Cluster |
| --- | --- | --- |
| pede | Docker | Docker e `kubectl`; o Kind roda por `go run`, sem instalação |
| sobe | `make up REPLICAS=n`, ou `docker compose up --build` com `WAGER_REPLICAS` | `make cluster-up REPLICAS=n` |
| endereço | `localhost:8090`, o HAProxy | `localhost:8091`, o NodePort do Service |
| quem tira da rotação a réplica que não está pronta | a sonda de `/health/ready` do balanceador, a cada segundo | a readiness do pod, a cada 5 s |
| rótulo da réplica no Prometheus | `instance`, o IP e a porta | `pod`, o nome do pod |
| teto | 10 réplicas: o `server-template` do balanceador não enxerga mais | as conexões do PostgreSQL |
| desce | `make down` | `make cluster-down`, que devolve as réplicas do Compose |

`REPLICAS` é o mesmo parâmetro nos dois, com padrão três, e um valor que não seja inteiro maior ou igual a um é recusado antes de qualquer passo, nomeando a variável.

### No Compose

`make up` sobe a stack inteira com o número pedido. As réplicas não publicam porta: o balanceador as descobre pelo DNS do Docker e só encaminha para a que responde ready ([ADR 0033](adr/0033-replicas-do-compose-atras-de-um-haproxy.md)).

Uma réplica parada à mão — `docker stop` ou `docker kill` — não volta sozinha, porque o serviço não tem política de reinício. `docker compose start wager` a devolve sem mexer no número. Um `docker compose up` sem `--scale` também a devolve, mas reconcilia o número com `WAGER_REPLICAS` e remove as réplicas que um `make up REPLICAS=n` subiu além dele; `make up REPLICAS=n` devolve as paradas e mantém o número.

### No cluster

`make cluster-up` parte do mesmo `docker compose up`, que aplica o schema e provisiona o broker, para as réplicas do Compose, cria o cluster Kind se ele não existe, liga o nó à rede do Compose, carrega as imagens no nó, gera o Secret e os ConfigMaps das mesmas variáveis e arquivos que o Compose lê, roda a migration como Job e só então aplica as réplicas e as leva ao número pedido ([ADR 0034](adr/0034-cluster-kind-sobre-os-servicos-do-compose.md)). Rodar de novo aplica o que mudou nos arquivos, troca as réplicas pela imagem do checkout e ajusta o número:

```bash
make cluster-up              # três réplicas
make cluster-up REPLICAS=5   # cinco, sobre o mesmo cluster
kubectl --context kind-junglegaming -n junglegaming get pods -l app=wager
kubectl --context kind-junglegaming -n junglegaming logs -f deploy/wager
```

Um Job que falha interrompe a subida, nomeia o Job e mostra o log dele, sem aplicar réplica nova. As séries de cada pod chegam ao Prometheus do Compose por um agente dentro do cluster ([ADR 0035](adr/0035-series-das-replicas-por-agente-com-escrita-remota.md)), então o painel "Liquidação" e os alertas veem as réplicas sem edição.

O teto do cluster são as conexões do PostgreSQL. Cada pod abre um pool do tamanho padrão do `pgxpool`, o maior entre quatro e o número de CPUs do nó — dezesseis no host em que isto foi medido —, e o PostgreSQL aceita 300, divididas também com a suíte de jornada e os cenários. Passar do teto aparece como réplica que não fica pronta e como aquisições no pool vazio, e não como perda: a conexão recusada é falha transitória.

### De um para o outro

Enquanto o cluster existe, as réplicas do Compose ficam paradas e o balanceador em `8090` responde 503, sem destino. O caminho de volta é `make cluster-down`, que apaga o cluster e devolve as réplicas do Compose, no número pedido, a `localhost:8090`. Um `docker compose up` à mão com o cluster de pé também as devolveria, em silêncio, e elas passariam a disputar a outbox e a fila com os pods. `make down` apaga o cluster, quando ele existe, antes de descartar os volumes do Compose, porque um nó ligado à rede impede o Compose de removê-la.

## A carga

```bash
make load                                # 60 s sobre as três réplicas do Compose
make load LOAD_KILL=forced               # com uma réplica morta à força no meio da janela
make load TARGET=cluster LOAD_KILL=graceful
make load REPLICAS=5 LOAD_CONCURRENCY=48
```

`make load` sobe o alvo — `make up` no Compose, que devolve as réplicas paradas por uma execução anterior, ou `make cluster-up` — e roda `scripts/loadtest`, um módulo só com a biblioteca padrão ([ADR 0036](adr/0036-teste-de-carga-com-veredito-e-resolucao-pela-chave.md)). Os parâmetros são `TARGET` (`compose` ou `cluster`), `REPLICAS`, `LOAD_DURATION` (60 s), `LOAD_CONCURRENCY` (32 workers), `LOAD_WALLETS` (100) e `LOAD_KILL` (`graceful`, `forced` ou vazio). O código de saída é o veredito, e não há meta de vazão: nenhum número de throughput faz a execução passar ou falhar.

O que ela faz:

1. Abre as carteiras, cada uma para um jogador novo, com 10000.00.
2. Durante a janela, cada worker envia `BET` (metade), `WIN` (dois quintos) e `LOSS` pela rota de apostas, com quantia de 1.00 a 5.00 e chave de idempotência própria. Um quarto das chegadas cai em quatro carteiras quentes, que as réplicas disputam; uma chegada em vinte reenvia uma operação anterior, com a mesma chave e o mesmo corpo. A mistura sai de uma semente, e cada worker mantém uma conexão, para as chegadas se espalharem pelas réplicas.
3. Com `LOAD_KILL`, no meio da janela, para (`docker stop`, ou `kubectl delete pod`) ou mata (`docker kill`, ou `kubectl delete pod --grace-period=0 --force`) a primeira réplica do alvo.
4. Cada chegada sem resposta decidida — falha de transporte ou 5xx — é reenviada pela própria chave, depois da janela, até responder o replay do que foi gravado ou a primeira conclusão. Qualquer outra resposta que não seja decidida — 400, 401, 403, os dois conflitos de idempotência — é defeito e falha a execução.
5. Confere cada carteira: saldo, versão e lançamentos contra o que a própria carga contou, e a reconciliação consistente.
6. Espera 15 s, para o Prometheus cobrir o fim da janela, lê o que as réplicas contaram, e espera a outbox drenar, por até 10 minutos.

A execução falha nomeando a carteira, o campo, o esperado e o obtido, e também com conflito de versão acima de zero, com uma réplica pedida que não decidiu chegada, com a outbox que não drena no prazo, e com o Prometheus fora ou uma série ausente — nunca publicando zero no lugar.

### O relatório

Sai na tela e em `.quality/load/report.json`.

| Campo | O que é | De onde vem |
| --- | --- | --- |
| `durationSeconds`, `sent`, `decided`, `throughputPerSecond` | a janela, as chegadas enviadas, as decididas, e as decididas por segundo | a carga |
| `latencyMilliseconds` | p50, p95 e p99 das chegadas decididas, exatos sobre todas as amostras, medidos no cliente | a carga |
| `errors`, `rejections`, `replays` | chegadas sem resposta decidida, rejeições por `failureCode`, e respostas de replay | a carga |
| `versionConflicts` | conflitos de versão somados sobre as réplicas | `wager_retries_total{reason="version_conflict"}` |
| `outboxOldestPendingMaxSeconds` | o maior atraso da outbox durante a execução | `wager_outbox_oldest_pending_age_seconds` |
| `outboxDrainSeconds` | quanto a outbox levou para ler zero pendentes depois da janela, na resolução do scrape | `wager_outbox_pending_events` |
| `poolEmptyAcquires` | aquisições de conexão que acharam o pool vazio | `wager_db_pool_empty_acquires_total` |
| `decidedByReplica` | chegadas decididas por réplica, pelo `instance` no Compose e pelo `pod` no cluster | `wager_settlements_total` mais `wager_duplicates_total`, da origem `http` |
| `kill` | a réplica, o modo e o instante, quando houve | a carga |
| `passed`, `failures` | o veredito, e cada achado nomeado | a carga |

Um contador do servidor é lido como o maior valor dele na janela menos o valor no início, com a série ausente no início valendo zero: `increase()` sobre uma série que nasce dentro da janela lê a primeira subida como nenhuma, e é assim que nasce a série de conflito de versão.

### O que foi medido

Num host de 16 CPUs e 11 GB, com a stack inteira no mesmo Docker, janela de 60 s, 100 carteiras e a semente 1:

| Alvo | Réplicas | Workers | Morte | Decididas/s | p50 | p95 | p99 | Erros | Conflitos | Pool vazio | Maior atraso da outbox | Drenagem |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Compose | 3 | 32 | — | 1 377 | 15,9 ms | 62,3 ms | 135,0 ms | 0 | 0 | 30 688 | 69,9 s | 319 s |
| Compose | 3 | 32 | graciosa | 1 801 | 11,7 ms | 42,5 ms | 99,0 ms | 0 | 0 | 21 884 | 72,8 s | 390 s |
| Compose | 3 | 32 | forçada | 1 783 | 12,0 ms | 42,5 ms | 95,8 ms | 0 | 0 | 20 949 | 71,0 s | 408 s |
| Compose | 5 | 48 | — | 1 911 | 12,2 ms | 90,0 ms | 247,1 ms | 0 | 0 | 1 545 | 70,5 s | 429 s |
| Cluster | 3 | 32 | — | 1 686 | 12,7 ms | 47,9 ms | 114,5 ms | 0 | 0 | 2 365 | 71,5 s | 377 s |
| Cluster | 3 | 32 | graciosa | 1 931 | 11,8 ms | 41,9 ms | 95,6 ms | 0 | 0 | 4 791 | 70,2 s | 474 s |
| Cluster | 3 | 32 | forçada | 1 922 | 12,1 ms | 41,3 ms | 89,4 ms | 0 | 0 | 24 138 | 68,1 s | 447 s |

Todo veredito passou: cada carteira fechou em saldo, versão e lançamentos, e cada réplica pedida decidiu chegadas — no cluster com parada graciosa, o pod substituto apareceu como uma quarta réplica. Como ler esses números:

- **A vazão para no banco.** Cinco réplicas decidem o mesmo que três; o que cresce com elas é a cauda, porque 48 workers disputam as mesmas quatro carteiras quentes, cujo lock serializa as operações de cada uma.
- **As duas mortes não deixaram chegada sem resposta.** No Compose, o balanceador reenviou a outra réplica o que estava na morta; no cluster, a remoção do pod, graciosa ou forçada, também não deixou erro. A resolução pela chave não teve o que resolver nessas execuções: ela é o caminho para a morte que pega um pedido em voo, e é provada pelos testes do módulo.
- **O pool vazio** aparece em até um terço das chegadas com três réplicas, e cai a 1 545 com cinco: cada réplica abre dezesseis conexões, e os 32 workers mais os envios da outbox passam disso. É espera por conexão, sem erro.
- **A outbox não acompanha a carga, e drena depois.** Ela publica um evento por vez por carteira, e cada uma das quatro carteiras quentes recebe ~200 eventos/s, contra os ~70–95 que um envio por vez entrega; no agregado, o SNS do LocalStack publica ~400–500 mensagens/s, contra as ~3 mil que a carga grava. O pendente mais antigo chega a ~70 s, e o alerta `OutboxOldestPendingTooOld` dispara durante toda carga ([ADR 0038](adr/0038-relay-paralelo-e-encadeado-por-carteira.md)).
- **No cluster, o balanceamento é por conexão.** Os pods decidiram parcelas desiguais, e com poucos workers para muitas réplicas uma delas pode ficar sem conexão nenhuma — o veredito reprova isso em vez de passar calado. A regra prática é pelo menos dez workers por réplica: `make load TARGET=cluster REPLICAS=5 LOAD_CONCURRENCY=48`.

## Exercícios de falha

Cada exercício foi executado como está escrito, e o que ele diz é o que se viu. As pausas e a divergência têm o mesmo comando nos dois modos, porque os serviços pausados são os do Compose; foram executadas sobre as réplicas do Compose, e o banco pausado também sobre as do cluster, para registrar a diferença entre o balanceador e o Service.

### Uma réplica morta sob carga

```bash
make load LOAD_KILL=graceful                  # docker stop na primeira réplica, no meio da janela
make load LOAD_KILL=forced                    # docker kill
make load TARGET=cluster LOAD_KILL=graceful   # kubectl delete pod
make load TARGET=cluster LOAD_KILL=forced     # kubectl delete pod --grace-period=0 --force
```

**O que se vê:** o relatório nomeia a réplica, o modo e o instante — aos 30 s de uma janela de 60 —, e o veredito passa. Nas quatro execuções medidas, nenhuma chegada terminou em erro, e a réplica morta aparece em `decidedByReplica` com menos da metade das chegadas das outras. No Compose, o balanceador tirou a réplica da rotação e reenviou a outra o que estava nela. No cluster, o Deployment subiu um pod substituto; na parada graciosa ele decidiu chegadas e aparece como uma quarta réplica, e na forçada o substituto não chegou a decidir chegada dentro da janela.

**Volta:** no Compose, a réplica parada ou morta não volta sozinha — o serviço não tem política de reinício. `docker compose start wager` a devolve sem mexer no número; o próximo `make load` a devolve pelo `make up`. No cluster, o Deployment já a substituiu.

### Banco pausado

```bash
docker compose pause postgres
docker compose unpause postgres
```

**O que se vê no Compose:** `/health/ready` de cada réplica responde 503 e o live continua 200; em até 8 s o balanceador tira as três da rotação e responde 503 a tudo em `localhost:8090`, inclusive ao live, por não ter destino. O pedido que chegou antes disso fica esperando o banco e termina numa linha `wallet request refused`. As réplicas continuam `healthy` no `docker compose ps`, sem reinício, e nenhum alerta dispara.

**No cluster:** nos primeiros ~10 s os pods ainda constam prontos e o ready passa o 503 do processo; em ~18 s os três saem de prontos — três sondas de 5 s —, o Service fica sem endpoint, e `localhost:8091` deixa de responder, até o live. A contagem de reinícios não muda.

**Volta:** o `unpause`. No Compose, o balanceador devolve as réplicas na primeira sonda, em menos de 1 s; no cluster, o primeiro pod fica pronto em 1 s, e os três em 3 s.

### Broker pausado

```bash
docker compose pause localstack
docker compose unpause localstack
```

**O que se vê:** a readiness cai na hora, porque consulta a fila, e em ~3 s o balanceador responde 503 a tudo. Uma operação gravada antes da pausa tem os eventos devolvidos ao backoff: `wager_retries_total{component="outbox",reason="transient"}` sobe, e a linha continua na outbox, com o mesmo `eventId`. Nenhuma réplica reinicia, e nenhum alerta dispara numa pausa de 40 s — o da outbox pede o pendente mais antigo acima de 30 s por um minuto.

**Volta:** o `unpause`; as réplicas voltam em 2 s, e a outbox publica o que esperava, com zero pendentes no minuto seguinte.

### IdP pausado

```bash
docker compose pause keycloak
docker compose unpause keycloak
```

**O que se vê:** as réplicas continuam prontas, porque a readiness não consulta o IdP. Pedir um token novo falha por timeout, mas uma chamada com um token já emitido continua respondendo 201: o processo guarda a chave pública do IdP e só a busca de novo diante de uma chave que não conhece. Numa pausa de 30 s nenhum token venceu; um que vença durante a pausa não tem como ser renovado até o `unpause`.

**Volta:** o `unpause`; o token novo sai na hora.

### Divergência escrita por fora

Um saldo escrito como superusuário, sem lançamento, com os gatilhos desligados na transação — o estado que o papel da aplicação não consegue produzir:

```bash
wallet=$(docker compose exec -T postgres psql -U junglegaming -d junglegaming -tAc \
  "SELECT id FROM wallets ORDER BY id LIMIT 1")
docker compose exec -T postgres psql -U junglegaming -d junglegaming -c \
  "BEGIN; SET LOCAL session_replication_role = replica;
   UPDATE wallets SET balance_cents = balance_cents + 12345 WHERE id = '$wallet'; COMMIT;"
```

**O que se vê:** o observador de divergência acha a carteira na passagem dele — 97 s depois, com mil carteiras no banco —, e uma réplica loga `wallet reconciliation diverged` com o `walletId` e `BALANCE_MISMATCH`; `wager_reconciliation_divergences_total{origin="watch"}` sobe, e `ReconciliationDivergenceFound` dispara, aqui em 116 s. O saldo não é corrigido: o observador só lê.

**Volta:** o mesmo `UPDATE`, subtraindo o que foi somado. `POST /wallets/{id}/reconciliation` volta a responder consistente, e o alerta se apaga sozinho 15 minutos depois da última passagem que achou a divergência — a janela da regra.

### O veredito que falha não tem exercício

A reconciliação que não produz veredito — a soma do ledger ou a diferença fora do `int64` — só existe com escrita que contorna as constraints, e desfazê-la exige desligar os gatilhos do ledger, que é justamente o que protege o resto dos dados. A prova do alerta `ReconciliationVerdictFailed` é o teste das regras, `make rules-test`, e a da série são os testes de unidade de quem a move ([ADR 0037](adr/0037-serie-e-alerta-do-veredito-que-falha.md)).
