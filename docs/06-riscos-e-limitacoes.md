# Riscos e limitações

O que está declarado em vez de apresentado como coberto. Cada item diz o que falta, por que ainda falta e o que o fecharia.

## Não implementado

O que não está nesta tabela está implementado, e cada rota, worker e invariante tem caso na suíte de jornada. Os oito cenários de concorrência que o enunciado exige têm pacote próprio, `internal/e2e/scenarios`, com várias instâncias independentes do processo, e o comando de cada um está no `README.md`.

| O quê | Estado | Nota |
| --- | --- | --- |
| Consumidor dos eventos | não existe | O tópico `wallet-events.fifo` é provisionado sem subscription, de propósito. A suíte de jornada anexa um assinante só pelo tempo do caso. |
| Réplicas em orquestrador, o guia de operá-las e o teste de carga | não existe | O Compose sobe três réplicas como processos separados, atrás de um balanceador ([ADR 0033](adr/0033-replicas-do-compose-atras-de-um-haproxy.md)), e os cenários com várias instâncias rodam por `make scenarios`, dentro do binário de teste. Falta o cluster Kind com a migration como Job, o guia de falha e o teste de carga sobre as réplicas. |
| Notificação dos alertas | não existe, de propósito | As duas regras vivem no Prometheus e aparecem no Grafana; não há Alertmanager, porque num ambiente local não há para onde notificar ([ADR 0025](adr/0025-alertas-como-regras-do-prometheus-testadas.md)). |

## Lacunas de verificação

**A amarração das interfaces de comportamento não tem teste.** `problem` declara `retryable`, `defective` e `replayed` e o caso de uso as satisfaz por estrutura ([ADR 0008](adr/0008-interfaces-de-comportamento-no-consumidor.md)). Nenhum teste passa `submitwager.ErrOutcomeInFlight` ou `ErrRaceUnresolved` por `problem.From`; o teste de `problem` usa um fake local. Um rename em qualquer dos lados degrada `503` com `Retry-After` em `500` sem que nada fique vermelho. O que fecha: um teste em `problem_test.go` sobre os erros reais, e uma assertiva anônima no produtor.

**A forma do identificador de principal da nuvem nunca é exercitada localmente.** O broker local registra o identificador da conta; a AWS registra o do principal ([ADR 0019](adr/0019-mapa-de-remetentes-pela-identidade-observada.md)). O valor é opaco e só comparado por igualdade dentro do mapa, então o risco é baixo — mas um mapa com o valor errado manda toda mensagem legítima para a DLQ, e é a linha de log com a identidade observada que corrige.

**Os cenários obrigatórios rodam as instâncias dentro de um processo só.** Cada instância é um grafo do Fx independente, com pool, porta e componentes de fundo próprios, e nenhuma garantia do sistema passa pela memória ([ADR 0027](adr/0027-instancias-como-grafos-do-fx-no-binario-de-teste.md)). Mas elas dividem o runtime do Go, e um estado de pacote de que um desfecho dependesse seria compartilhado por todas: os cenários passariam onde processos separados falhariam. O isolamento de memória pelo sistema operacional não é provado por eles. O que fecha: o teste de carga sobre réplicas que são processos separados — as do Compose já existem, sem carga sobre elas.

**O piso de cobertura tem margem estreita.** 70% em `internal/platform` e 80% em `internal/app`. Os caminhos de I/O de `postgres`, `probe` e `telemetry` são cobertos pela suíte de integração, que não entra nesse cálculo.

## Guardas para o que não deveria acontecer

Três erros existem para um estado que o desenho torna inalcançável, e respondem `503` com `Retry-After` em vez de cair por baixo em silêncio. Cada um tem razão própria em `wager_retries_total` — `version_conflict`, `outcome_in_flight` e `race_unresolved` —, e se um deles aparecer ali, é algo a investigar, não a ignorar.

| Guarda | Estado impossível | Por que é impossível |
| --- | --- | --- |
| `storage.ErrLostWrite` | `UPDATE` sob `FOR UPDATE` afetando zero linhas | o lock serializa; a versão é a assertiva |
| `submitwager.ErrOutcomeInFlight` | linha lida em `PENDING` | o `CHECK` recusa gravar esse status |
| `submitwager.ErrRaceUnresolved` | vencedora do índice de chave ausente na releitura | um `INSERT` só recebe `23505` depois que a outra transação commitou, e nada apaga transações |

## Custos aceitos

- **`LOSS` com referência carrega a citada e a descarta.** O construtor não proíbe um `LOSS` de citar; `citedFor` busca a operação e `Loss` a ignora. Um `SELECT` gasto, sem efeito. Proibir exigiria um token novo para um problema que não existe em produção.
- **Identidades cunhadas e não usadas.** Quatro UUIDs por chegada — transação, lançamento, dois eventos — mesmo quando o desfecho usa dois. O preço de o domínio nunca chamar um minter.
- **O alerta de divergência não vê a carteira cuja reconciliação não cabe em `int64`.** A soma do ledger ou a diferença para o saldo passando de 92 quatrilhões de reais falham o veredito em vez de contá-lo ([ADR 0022](adr/0022-reconciliacao-em-uma-sentenca-com-veredito-no-caso-de-uso.md), [ADR 0031](adr/0031-diferenca-fora-do-int64-falha-a-reconciliacao.md)). Só uma escrita que contorna a aplicação chega lá, e o observador deixa uma linha de falha com o `walletId` a cada passagem, mas nenhuma série a conta. O que fecha: uma série do veredito que falha e um alerta sobre ela, no degrau de operação.
- **Vinte linhas duplicadas entre dois runners de fundo** ([ADR 0011](adr/0011-tres-runners-de-fundo-separados.md)).
- **As leituras não têm prazo próprio, e o pool não tem teto explícito.** A reconciliação é a primeira rota cujo custo cresce com o ledger da carteira ([ADR 0022](adr/0022-reconciliacao-em-uma-sentenca-com-veredito-no-caso-de-uso.md)), e o observador de divergência a chama em lote, a cada turno ([ADR 0024](adr/0024-observador-de-divergencia-por-cursor-em-memoria.md)). O plano entra pelo índice único da carteira e lê só as linhas dela, com `sequence_number` já pré-ordenado, então a memória não cresce com o ledger; e o contexto da requisição cancela a consulta quando o cliente desiste, devolvendo a conexão ao pool. O que falta é um prazo do lado do servidor: com carteira grande, ou com leituras simultâneas bastantes, cada uma ocupa uma conexão do pool — cujo padrão do `pgxpool` é o maior entre quatro e o número de CPUs — e `SubmitWager` passa a esperar por conexão. Isso agora é visível: `wager_db_pool_connections` por estado e `wager_db_pool_empty_acquires_total`, as aquisições que acharam o pool vazio, estão no painel. Fixar um `statement_timeout` agora seria escolher o número sem medida, e ele atingiria também a unit of work, onde um prazo estourado é falha de infraestrutura no caminho do dinheiro. O que fecha: o teste de carga do degrau seguinte, que lê essas séries antes de escolher o prazo, o tamanho do pool, e o intervalo e o lote do observador.

## Ambiente local

- O LocalStack community não persiste: qualquer reinício esvazia filas e tópico. A subida e o reinício do broker pelo Compose rodam o apply de novo; um reinício por fora dele, como `docker restart`, não, e até alguém rodar `make provision` os eventos da outbox vão a um tópico que não existe e morrem na décima recusa ([ADR 0032](adr/0032-apply-do-broker-como-servico-do-compose.md)). O IAM dele é parcial — o principal é criado, mas a política pode não ser aplicada como na AWS.
- O Compose sobe três réplicas do processo atrás de um HAProxy, que só encaminha para a que responde ready e enxerga até dez ([ADR 0033](adr/0033-replicas-do-compose-atras-de-um-haproxy.md)). As réplicas em Kubernetes ficam para Kind ou k3d, com a migration como Job que roda uma vez antes delas — decidido, sem manifesto nem guia ainda, como a tabela acima registra.
- O realm de teste não tem mapper de audience, e os clientes estão com `fullScopeAllowed`. A borda não confere `aud` ([ADR 0020](adr/0020-jwks-separado-do-issuer.md)).
- O `causationId` do envelope sai omitido em todo commit que nenhuma mensagem causou: a operação por HTTP tem só `correlationId`, e o commit diferido do worker de referência é disparado pelo prazo, não pela mensagem. A travessia entre os dois commits é o identificador da transação, que o primeiro evento carrega e o segundo usa como correlação.
