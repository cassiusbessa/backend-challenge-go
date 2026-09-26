# Conceitos transversais

O que atravessa as três camadas: como um erro viaja, quem pode o quê, o que sai em log e trace, e como o processo sobe e desce.

## Erros

Duas classes, e elas não se misturam.

| | Rejeição de negócio | Falha de infraestrutura |
| --- | --- | --- |
| O que é | uma regra recusou: resultado esperado | o que não devia acontecer: banco fora, prazo, broker |
| Tipo | `wager.Rejection`, com token do catálogo | `*fault.Error` na cadeia, com stack |
| Span | ok | erro |
| Stack | nenhuma | capturada uma vez, na fronteira onde foi vista primeiro |
| HTTP | `422` + `failureCode` | `503` sem token |
| Grava linha? | quando vem de uma ação sobre a carteira travada | nunca: a transação SQL desfaz |

A fronteira entre as duas é desenhada no adaptador SQL: `postgres.wrap` lê o erro do `pgx` e devolve **ou** uma recusa do contrato (violação de índice único → token, sem stack) **ou** `fault.Wrap` (qualquer outra coisa → stack). Nada embrulha uma classe dentro da outra ([ADR 0010](adr/0010-stack-capturada-uma-vez.md)).

### A cadeia

Cada fronteira de pacote embrulha com `fmt.Errorf("<operação>: %w", err)`. A mensagem final lê como trilha — `submit wager: insert transaction: connection refused`. Embrulhar dentro do mesmo pacote engrossa a cadeia sem informação, então `apply`, `record` e `write` passam o erro cru.

Nada é mutado no caminho: cada embrulho é um valor novo que aponta para o anterior, e `errors.Is` e `errors.As` só leem. Um `INSUFFICIENT_FUNDS` ganha dois nós — a `Rejection` em `wager.translate` e o `submit wager:` na saída do caso de uso — e é lido cinco vezes, uma por camada, cada uma perguntando só o que decide.

### A classificação na borda

```mermaid
flowchart TD
    E["erro que chegou à borda"] --> R{"errors.As<br/>wager.Rejection?"}
    R -- "sim" --> B["422 business-rejection<br/>failureCode = token"]
    B --> RP{"IdempotentReplay()?"}
    RP -- "sim" --> B2["+ idempotentReplay: true"]
    R -- "não" --> N{"sentinela nomeada?"}
    N -- "ErrInvalidInput" --> C400["400 invalid-input<br/>detail = nome do campo"]
    N -- "ErrWalletExists" --> C409["409 wallet-already-exists"]
    N -- "ErrNotFound (família)" --> C404["404 not-found"]
    N -- "ErrNotPermitted" --> C403["403 unauthorized"]
    N -- "ErrLostWrite" --> C503a["503 unavailable · stack"]
    N -- "nenhuma" --> M{"marcador de<br/>comportamento?"}
    M -- "RetryShortly()" --> C503b["503 retryable<br/>Retry-After: 1 · span ok"]
    M -- "Defect()" --> C500a["500 internal · stack"]
    M -- "fault.Error na cadeia" --> C503c["503 unavailable · stack"]
    M -- "nenhum" --> C500b["500 internal · stack<br/>ninguém classificou: defeito nosso"]
```

A ordem importa: uma sentinela que a borda nomeia vence um marcador, para uma condição com resposta conhecida nunca ser lida como indisponibilidade. E o que não carrega marcador nenhum é `500`, não `503`: responder "tente de novo" a um defeito diria ao provedor para repetir o que nunca vai passar.

Os três marcadores — `retryable`, `defective`, `replayed` — são interfaces de um método declaradas em `problem` e satisfeitas por tipos do caso de uso que não sabem disso ([ADR 0008](adr/0008-interfaces-de-comportamento-no-consumidor.md)).

### Três perguntas, três camadas

| Pergunta | Quem faz | Consequência |
| --- | --- | --- |
| tem linha? | `submitwager.reject` | commit ou rollback |
| tem token? | `problem.From` | `422` com `failureCode`, ou outra classe |
| já foi respondido antes? | `problem.isReplay` | `idempotentReplay: true` |

Um campo único não caberia nas três: `AMOUNT_NOT_ALLOWED_FOR_KIND` tem token e não tem linha, porque o `CHECK` recusaria a linha; `INSUFFICIENT_FUNDS` tem os dois; o replay de uma rejeição tem os três.

### Quem loga

Quem retorna erro não loga. Loga quem decide o desfecho — o `Reporter` da borda HTTP, o consumidor da fila, o worker — e loga uma vez, com `fault.Stack(err)` no atributo `stack` quando há. `Detail` no corpo de erro nomeia o campo recusado e nunca o valor: quantia, saldo, chave e credencial não voltam.

## Autorização

O Keycloak emite tokens por `client_credentials`; este serviço não cadastra senha e não emite token. A borda valida assinatura, emissor e validade contra o JWKS do realm, localmente, e lê a claim `azp` — o `client_id` para quem o token foi emitido.

Esse `client_id` é uma **credencial**. O `providerId` é uma **entidade de negócio**. O mapa versionado em `deploy/local/clients.yaml` liga um ao outro, e é a única fonte do provedor pelo qual um request fala:

```
Bearer eyJ…  →  azp = "provider-a"  →  mapa  →  Client{Provider, providerId: "provider-a"}
                                                        ↓
                                    speaksFor: providerId do token == providerId do corpo
```

Um provedor pode ter vários clientes — dois datacenters, uma rotação de segredo — e todos operam as mesmas transações. Localmente o `client_id` e o `providerId` são a mesma string, o que esconde a diferença; em produção não são.

| Papel | Pode | Não pode |
| --- | --- | --- |
| **Interno** | abrir carteira, ler carteira, reconciliar | enviar aposta |
| **Provedor** | enviar aposta, ler a própria transação (inclusive no replay) | abrir carteira, ler saldo, ledger ou reconciliação, ler transação alheia |

Corpo de outro provedor recusa com `403`, sem movimento e sem revelar se a transação existe — uma transação alheia e uma inexistente saem pelo mesmo `404`, porque o provedor é parte da consulta e não de uma checagem depois. `playerId` que não é o dono da carteira rejeita com `PLAYER_WALLET_MISMATCH`, sem movimento.

Credencial ausente, inválida ou expirada é `401`; identidade válida sem permissão é `403`. As duas em problem details, sem dado financeiro. `/health/*` e `/metrics` são públicos.

Na fila não há token: a identidade vem do broker junto da mensagem, e a recusa é ida para a DLQ. O mapa `deploy/local/queue-senders.yaml` é chaveado pela identidade que o consumidor observa, com a lista de provedores permitidos por remetente ([ADR 0019](adr/0019-mapa-de-remetentes-pela-identidade-observada.md)).

## Observabilidade

**Log** em JSON com lista branca de campos: `correlationId`, `trace_id`, `span_id`, `messageId`, `eventId`, `transactionId`, `walletId`, `providerId`, `kind`, `status`, `failureCode`. O filtro é do handler, não da chamada: atributo fora da lista é descartado antes da saída, então header de autorização, token, corpo, quantia e saldo não vazam nem por engano. Sucesso loga só ids; rejeição, conflito, DLQ e divergência sempre logam.

**Correlação**: no HTTP, `X-Correlation-Id` vale se for token opaco curto, senão o `trace_id`; na fila sem correlação, o `messageId`. Decidida uma vez na borda, antes do handler, e carregada no contexto — o que a operação grava a leva.

**Trace** por OTLP, com propagação W3C no header HTTP e em atributo da mensagem. Um span da entrada, um do caso de uso, um da unit of work fechado no commit; sem span por query. O nome do span é o padrão da rota, nunca o path com identidade. `REJECTED` deixa o span ok; erro de span é infraestrutura, conflito de versão ou DLQ. A outbox grava `trace_id` e `span_id` do commit, e o relay abre span próprio ligado a eles por *link*, não por paternidade — aquele span fechou no commit, e depois de um restart nem existe.

**Métricas** em `/metrics`, Prometheus, com exemplar de `trace_id` na latência para o p99 abrir o trace. Sem `walletId` nem `providerId` em rótulo. Resultado por status e origem, rejeição por `failureCode`, duplicata, retry, DLQ por razão, profundidade da DLQ lida uma vez por turno, conflito de versão, atraso e quantidade da outbox, idade da espera mais antiga, saturação do pool, heap e goroutines. `pprof` escuta em porta própria.

**Saúde**: `/health/live` responde pelo processo; `/health/ready` consulta PostgreSQL e SQS com prazo curto, e ready falho não derruba o live. Os dois e `/metrics` ficam fora do span e do log, porque batem de segundo em segundo.

## Ciclo de vida

O Uber Fx compõe o processo, e `internal/platform/app` é o único pacote que o conhece. A configuração é lida e validada antes de qualquer porta abrir.

A telemetria é a exceção e sobe **antes** de todo o resto, no construtor dela, porque o Fx roda todo construtor antes de todo hook: um start registrado como hook entregaria aos componentes um provider que ele mesmo substituiria depois ([ADR 0021](adr/0021-telemetria-iniciada-no-construtor-antes-dos-hooks.md)).

Subida, nesta ordem: validação da configuração → pool do PostgreSQL → sonda da fila → sonda do tópico → fila de entrada → worker de referência → relay da outbox → consumidor da fila → servidor HTTP. Nenhum componente de fundo segura a subida com fila vazia.

Descida, na ordem inversa, e cada parada com uma **fatia** de `SHUTDOWN_TIMEOUT` (20 s por padrão). O Fx entrega a todos os hooks o mesmo contexto e retorna quando ele vence, pulando o que não alcançou — uma parada que consome o orçamento inteiro leva as de trás consigo. As fatias moram ao lado do registro, e a subida é recusada se o orçamento não paga a soma delas ([ADR 0012](adr/0012-flush-de-telemetria-fora-do-lifecycle.md)).

No `SIGTERM`:

| Componente | Faz | Não faz |
| --- | --- | --- |
| Servidor | para de aceitar; conclui o pedido em curso na sua fatia | |
| Worker de referência | para de reivindicar espera nova | não corta a decisão em curso |
| Relay | para de reivindicar linha; conclui o envio em curso dentro do lease | o sinal não cancela um envio já reivindicado — quem corta é o prazo do processo |
| Consumidor | para de buscar; conclui a decisão em curso no prazo dela | a mensagem cortada pelo prazo volta com visibilidade zero |
| Telemetria | descarrega o buffer **depois** do ciclo de vida, com 3 s próprios; uma subida que falhou também descarrega | não falha o processo se não conseguir |

## Dinheiro

`Money` é `int64` de centavos com a moeda no tipo, escala fixa de duas casas, sem `float` em caminho nenhum. Entra e sai como string decimal. Soma, subtração, negação e parse verificam overflow; comparar ou operar exige a mesma moeda. Vazio, `NaN`, `Infinity`, notação científica, mais de duas casas e negativo são recusados na entrada, não arredondados. O zero value não tem moeda e é inválido, e é essa ausência que o sistema inteiro usa para distinguir *ausente* de *zero* — na coluna, no corpo e no evento. O modelo está em [02-dominio](02-dominio.md).
