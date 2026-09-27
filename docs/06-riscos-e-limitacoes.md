# Riscos e limitações

O que está declarado em vez de apresentado como coberto. Cada item diz o que falta, por que ainda falta e o que o fecharia.

## Não implementado

O que não está nesta tabela está implementado, e cada rota, worker e invariante tem caso na suíte de jornada. Os oito cenários de concorrência que o enunciado exige têm pacote próprio, `internal/e2e/scenarios`, com várias instâncias independentes do processo, e o comando de cada um está no `README.md`.

| O quê | Estado | Nota |
| --- | --- | --- |
| Consumidor dos eventos | não existe | O tópico `wallet-events.fifo` é provisionado sem subscription, de propósito. A suíte de jornada anexa um assinante só pelo tempo do caso. |
| Notificação dos alertas | não existe, de propósito | As três regras vivem no Prometheus e aparecem no Grafana; não há Alertmanager, porque num ambiente local não há para onde notificar ([ADR 0025](adr/0025-alertas-como-regras-do-prometheus-testadas.md)). |
| Publicação em lote por carteira | não existe | O relay publica um evento por vez por carteira ([ADR 0038](adr/0038-relay-paralelo-e-encadeado-por-carteira.md)). Uma carteira que recebe mais eventos por segundo do que um envio por vez entrega acumula outbox, como as carteiras quentes da carga. O que fecha: reivindicar e publicar até dez eventos de uma carteira por chamada, que o SNS FIFO entrega em ordem — muda a regra de não começar o seguinte antes de confirmar o anterior. |

## Lacunas de verificação

**A forma do identificador de principal da nuvem nunca é exercitada localmente.** O broker local registra o identificador da conta; a AWS registra o do principal ([ADR 0019](adr/0019-mapa-de-remetentes-pela-identidade-observada.md)). O valor é opaco e só comparado por igualdade dentro do mapa, então o risco é baixo — mas um mapa com o valor errado manda toda mensagem legítima para a DLQ, e é a linha de log com a identidade observada que corrige.

**Os cenários obrigatórios rodam as instâncias dentro de um processo só.** Cada instância é um grafo do Fx independente, com pool, porta e componentes de fundo próprios, e nenhuma garantia do sistema passa pela memória ([ADR 0027](adr/0027-instancias-como-grafos-do-fx-no-binario-de-teste.md)). O teste de carga repete a disputa com réplicas que são processos do sistema operacional, no Compose e no cluster, inclusive matando uma no meio, e o veredito por carteira passou em todas as execuções medidas ([07 · Operação](07-operacao.md)). O que ele não repete é cada cenário com a falha no ponto exato — o commit cuja remoção da mensagem nunca chega ao broker, a espera por referência que atravessa um reinício —, e esses continuam provados só com instâncias no mesmo binário.

**O piso de cobertura tem margem estreita.** 70% em `internal/platform` e 80% em `internal/app`. Os caminhos de I/O de `postgres`, `probe` e `telemetry` são cobertos pela suíte de integração, que não entra nesse cálculo.

## Guardas para o que não deveria acontecer

Três erros existem para um estado que o desenho torna inalcançável, e respondem `503` com `Retry-After` em vez de cair por baixo em silêncio. Cada um tem razão própria em `wager_retries_total` — `version_conflict`, `outcome_in_flight` e `race_unresolved` —, e se um deles aparecer ali, é algo a investigar, não a ignorar. O teste de carga lê `version_conflict` sobre as réplicas e falha acima de zero; em todas as execuções medidas, com três e cinco réplicas disputando as mesmas carteiras e uma delas morta no meio, a série não subiu.

| Guarda | Estado impossível | Por que é impossível |
| --- | --- | --- |
| `storage.ErrLostWrite` | `UPDATE` sob `FOR UPDATE` afetando zero linhas | o lock serializa; a versão é a assertiva |
| `submitwager.ErrOutcomeInFlight` | linha lida em `PENDING` | o `CHECK` recusa gravar esse status |
| `submitwager.ErrRaceUnresolved` | vencedora do índice de chave ausente na releitura | um `INSERT` só recebe `23505` depois que a outra transação commitou, e nada apaga transações |

## Custos aceitos

- **`LOSS` com referência carrega a citada e a descarta.** O construtor não proíbe um `LOSS` de citar; `citedFor` busca a operação e `Loss` a ignora. Um `SELECT` gasto, sem efeito. Proibir exigiria um token novo para um problema que não existe em produção.
- **Identidades cunhadas e não usadas.** Quatro UUIDs por chegada — transação, lançamento, dois eventos — mesmo quando o desfecho usa dois. O preço de o domínio nunca chamar um minter.
- **Uma falha passageira do banco exatamente entre a página e o veredito acende o alerta de veredito por 15 minutos.** O alerta não tem `for`, porque um veredito que falha com a página respondendo já é achado; a linha de log com o `walletId` diz qual carteira foi ([ADR 0037](adr/0037-serie-e-alerta-do-veredito-que-falha.md)).
- **Sob a carga, a outbox fica minutos atrás, e o alerta dela dispara.** Nas execuções medidas, o pendente mais antigo chegou a ~70 s e a outbox drenou de 5 a 8 minutos depois de uma janela de 60 s. Duas razões, ambas de um evento por vez: cada carteira quente recebe ~200 eventos/s contra os ~70–95 que um envio por vez entrega, e o SNS do LocalStack publica ~400–500 mensagens/s no agregado, contra as ~3 mil que a carga grava ([ADR 0038](adr/0038-relay-paralelo-e-encadeado-por-carteira.md)). Nenhum evento se perde nem sai fora de ordem; o custo é atraso.
- **Vinte linhas duplicadas entre dois runners de fundo** ([ADR 0011](adr/0011-tres-runners-de-fundo-separados.md)).
- **As leituras não têm prazo próprio, e o pool não tem teto explícito.** A reconciliação é a primeira rota cujo custo cresce com o ledger da carteira ([ADR 0022](adr/0022-reconciliacao-em-uma-sentenca-com-veredito-no-caso-de-uso.md)), e o observador de divergência a chama em lote, a cada turno ([ADR 0024](adr/0024-observador-de-divergencia-por-cursor-em-memoria.md)). O plano entra pelo índice único da carteira e lê só as linhas dela, e o contexto da requisição cancela a consulta quando o cliente desiste. O teste de carga mediu o que o prazo, o pool e o observador fazem sob disputa ([07 · Operação](07-operacao.md)): com três réplicas, até um terço das aquisições achou o pool vazio — dezesseis conexões por réplica, o padrão do `pgxpool` para o número de CPUs deste host, contra 32 workers e os envios da outbox —, o que custou espera e nenhum erro, com p99 entre 89 e 135 ms; com cinco réplicas, o pool vazio caiu a 1 545 aquisições. O observador, no padrão de 5 s e lote de 50, produziu ~166 mil vereditos nas duas horas de carga, sem falha nem divergência. Nenhuma medida pediu mudar o prazo, o pool ou o observador: o pool vazio é espera, não perda, e fixar um `statement_timeout` atingiria também a unit of work, onde um prazo estourado é falha no caminho do dinheiro.

## Ambiente local

- O LocalStack community não persiste: qualquer reinício esvazia filas e tópico. A subida e o reinício do broker pelo Compose rodam o apply de novo; um reinício por fora dele, como `docker restart`, não, e até alguém rodar `make provision` os eventos da outbox vão a um tópico que não existe e morrem na décima recusa ([ADR 0032](adr/0032-apply-do-broker-como-servico-do-compose.md)). O IAM dele é parcial — o principal é criado, mas a política pode não ser aplicada como na AWS.
- O Compose sobe três réplicas do processo atrás de um HAProxy, que só encaminha para a que responde ready e enxerga até dez ([ADR 0033](adr/0033-replicas-do-compose-atras-de-um-haproxy.md)). O cluster Kind liga o nó à rede do Compose por fora da própria configuração, então `make down` o apaga antes de o Compose remover a rede, e um `docker compose up` à mão com ele de pé devolve as réplicas do Compose em silêncio ([ADR 0034](adr/0034-cluster-kind-sobre-os-servicos-do-compose.md)). As imagens entram no nó por arquivo, porque com o image store do containerd o `kind load docker-image` de uma imagem baixada falha.
- O realm de teste não tem mapper de audience, e os clientes estão com `fullScopeAllowed`. A borda não confere `aud` ([ADR 0020](adr/0020-jwks-separado-do-issuer.md)).
- O `causationId` do envelope sai omitido em todo commit que nenhuma mensagem causou: a operação por HTTP tem só `correlationId`, e o commit diferido do worker de referência é disparado pelo prazo, não pela mensagem. A travessia entre os dois commits é o identificador da transação, que o primeiro evento carrega e o segundo usa como correlação.
