# Arquitetura

Serviço de liquidação de apostas: um bounded context, um binário, várias réplicas. Provedores de jogo enviam transações por HTTP e por SQS; o serviço debita e credita carteiras, grava um ledger imutável e publica eventos.

## Estado da implementação

Este documento registra as decisões do desenho inteiro. Nem tudo está escrito em código ainda. O que já existe:

| Área | Estado |
| --- | --- |
| Ambiente compartilhado: Compose, Terraform local, realm do Keycloak | implementado |
| Processo: Fx, configuração, `/health/live`, `/health/ready`, log JSON, OTLP, `/metrics`, `pprof` | implementado |
| Domínio: identidades, `Money`, `Wallet`, `LedgerEntry`, `WagerTransaction` | implementado |
| Schema das três tabelas financeiras, com as constraints, e migration versionada aplicada antes das réplicas | implementado |
| Pool `pgx` compartilhado, unit of work em `READ COMMITTED`, repositórios de carteira, transação e lançamento | implementado |
| Borda HTTP de erro em `application/problem+json`, com o mapa de status no adaptador | implementado |
| Autorização por token do IdP nas rotas de carteira: `POST /wallets` e `GET /wallets/:walletId` | implementado |
| Idempotência persistente: chave com escopo no provedor, hash canônico do corpo de negócio, replay do resultado gravado e os dois conflitos decididos pelo índice único | implementado |
| Lock pessimista da carteira com guarda de versão, e escrita perdida tratada como falha transitória | implementado |
| `BET`, `LOSS` e `WIN` sem referência por HTTP, em `POST /wagering/transactions` e `GET /wagering/transactions/{transactionId}` | implementado |
| Autorização das rotas de aposta pelo papel de provedor, com o `providerId` do corpo conferido contra o cliente do token | implementado |
| `WIN` com referência, `REFUND`, `ROLLBACK`, `PENDING_REFERENCE` com o 202 da borda, TTL e worker da espera | implementado |
| Outbox no commit do saldo, relay com lease por carteira e publicação no tópico FIFO | implementado |
| Inbox e consumidor SQS | decidido, não implementado |
| Ledger paginado, reconciliação | decidido, não implementado |

A ordem de entrega está em `openspec/changes/`. As regras que governam cada decisão estão em `.claude/rules/`.

## Dinheiro

`Money` é valor imutável: quantia em `int64` de centavos, moeda no tipo, escala fixa de duas casas. Nenhum caminho usa `float32` ou `float64`.

O limite é o do `int64`: de `-92.233.720.368.547.758,08` a `92.233.720.368.547.758,07`. Soma, subtração, negação e parse verificam overflow e devolvem erro em vez de embrulhar.

Na borda externa, o contrato entra e sai como `{"amount":"25.00","currency":"BRL"}` — string decimal, nunca número JSON. São recusados vazio, `NaN`, `Infinity`, notação científica, escala maior que duas casas e valor negativo. Entrada inválida não é arredondada e não grava transação.

Na persistência, a quantia é `BIGINT` e a moeda é o código ISO 4217. Comparar ou operar exige a mesma moeda; moeda diferente rejeita com `CURRENCY_MISMATCH`.

Valor negativo existe em diferença interna. O saldo da carteira permanece maior ou igual a zero, garantido pelo agregado e por constraint no PostgreSQL. Zero é válido no saldo inicial e em `LOSS`; `BET`, `WIN`, `REFUND` e `ROLLBACK` exigem quantia maior que zero.

## Agregados

Duas raízes: `Wallet` e `WagerTransaction`. A transação guarda o `WalletID`, não a carteira.

`LedgerEntry` nasce dentro de `Debit` ou `Credit` e só é inserido — não é agregado, não é atualizado, não é apagado. A carteira expõe `Open`, `Debit` e `Credit`; os dois últimos delegam a um `move` privado, único lugar que calcula o saldo, monta o lançamento e atribui os campos. O estado seguinte é calculado por completo antes de qualquer atribuição: falha deixa o agregado intocado.

O pacote `wager` tem uma função por tipo de aposta, e nenhuma delas calcula saldo. `Bet` chama `Debit`. `Win` e `Refund` chamam `Credit`. `Loss` não chama a carteira. `Rollback` credita se a original foi `BET` e debita se foi `WIN` ou `REFUND`. O caso de uso escolhe a função pelo `kind`; essa escolha não grava SQL nem emite evento.

O domínio importa a biblioteca padrão e os próprios pacotes. Fx, `pgx`, HTTP e SQS ficam de fora. Erro de negócio é valor, com sentinela em `errors.Is` ou tipo em `errors.As`; `panic` não representa rejeição.

Inbox e outbox não são agregados: entram na mesma transação SQL, e o agregado não acumula evento pendente.

## Máquina de estados

Estados: `PENDING`, `PENDING_REFERENCE`, `PROCESSED`, `REJECTED`, `FAILED`. Não existe `PROCESSING`. Estado terminal não muda, e um replay devolve o resultado gravado sem reaplicar a operação.

`PENDING` é trabalho interrompido — a operação já tem todos os dados. No caminho feliz ele só existe em memória: o commit grava `PROCESSED`. `PENDING` não é status gravável.

`PENDING_REFERENCE` é espera pela operação citada, que ainda não chegou por outro canal. A linha fica no banco, a mensagem de entrada se conclui, e o worker de referência assume. Na borda HTTP essa aceitação tem desfecho próprio: **202**, com o recurso apontado e o estado no corpo, sem saldo observado e fora de problem details. Empilhá-la no 201 obrigaria o provedor a ler o corpo para saber se houve movimento financeiro, que é exatamente o que um código distinto evita.

`OPENING` com saldo positivo já nasce `PROCESSED`, com o lançamento. Saldo inicial zero não cria `OPENING` nem lançamento. `LOSS` termina `PROCESSED`, sem lançamento e sem incrementar a versão.

`REJECTED` é regra de negócio, sempre com `failureCode` estável. Falha transitória não é rejeição: desfaz a transação SQL e tenta de novo. `FAILED` só cabe numa linha já durável que não pode mais ser concluída por falha permanente de infraestrutura.

## Idempotência

A chave chega em `Idempotency-Key` ou em `data.idempotencyKey` e é gravada como veio, com escopo no provedor. Ela não é substituída por `{providerId}:{externalTransactionId}` — outro provedor com a mesma chave é outra operação.

O hash é SHA-256 em hex do JSON de negócio canônico: chaves em ordem alfabética, sem espaço sobrando, sem escapar `<`, `>` e `&`. Entram `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency` e, se houver, `referenceExternalTransactionId`. Ficam de fora a própria chave, headers, `messageId`, `occurredAt`, `type` e correlação. Antes do hash, a quantia passa pelos centavos e volta com duas casas, e UUID entra em minúsculas. Campo ausente sai do JSON; `null` não entra. Por isso HTTP e SQS do mesmo negócio produzem o mesmo hash.

- Mesma chave e mesmo hash: devolve o resultado gravado, com `idempotentReplay: true` e o saldo observado naquela hora, não o saldo de agora.
- Mesma chave e outro hash: `IDEMPOTENCY_CONFLICT`, sem linha nova.
- Mesmo `(providerId, externalTransactionId)` com outra chave: `DUPLICATE_EXTERNAL_TRANSACTION`, sem linha nova.

A garantia é do banco, não da memória: índices únicos por `(providerId, externalTransactionId)` e por `(providerId, idempotencyKey)`. Duas requisições paralelas disputam um insert; quem perde a constraint relê a linha e cai em replay ou conflito. A idempotência e o lançamento entram no mesmo commit.

Abertura de carteira não usa essa chave — a unicidade dela é jogador mais moeda.

## Concorrência

A unit of work usa `READ COMMITTED`. Antes de decidir qualquer coisa, `SELECT … FOR UPDATE` na linha daquela carteira. O `UPDATE` do saldo leva `WHERE id = $1 AND version = $versão lida`, e a versão sobe exatamente um quando o saldo muda.

Zero linhas atualizadas significa escrita que furou o lock: desfaz a transação SQL e tenta de novo. Isso é falha transitória, não rejeição de negócio.

Com o lock, a segunda aposta espera, lê o saldo já commitado e, se não couber, rejeita com `INSUFFICIENT_FUNDS`. É o caso do teste obrigatório: duas apostas de 80 contra saldo 100 terminam em uma processada, uma rejeitada, saldo 20 e um lançamento.

A ordem é sempre carteira, depois transação — inclusive no worker de referência. Linha pendente já reivindicada por outra réplica usa `SKIP LOCKED`. Carteiras diferentes não compartilham lock, então escalam em paralelo. Não há lock de tabela nem mutex no processo, e a correção não depende de haver uma instância só.

## Referência pendente

Ao entrar em `PENDING_REFERENCE`, `referenceDeadlineAt = agora + TTL` é gravado uma vez. O TTL padrão é 15 minutos, configurável. Quem encerra a espera é esse prazo, não um teto de tentativas.

O worker só tenta quando `nextAttemptAt` chegou, e reivindica a linha com lock para duas réplicas não concluírem a mesma espera. O backoff é exponencial com base 1s, fator 2 e teto 60s, com o intervalo sorteado entre zero e esse teto, e o instante agendado nunca passa do prazo. `attemptCount` é métrica.

Ele é o primeiro processo de fundo do binário e sobe no mesmo lifecycle do servidor HTTP, depois do pool. O turno tem dois passos, e a ordem é o ponto: a varredura escolhe candidatos **sem lock e fora de transação**, servida pelo índice parcial `(next_attempt_at) WHERE status = 'PENDING_REFERENCE'`; a decisão abre uma transação, trava a carteira, reivindica a linha com `FOR UPDATE SKIP LOCKED` e reconfere estado e prazo sob esse lock. O estado que vale é o relido, nunca o que a varredura viu. Reivindicar a transação antes da carteira inverteria a ordem contra a submissão.

Quem escolhe entre `REFERENCE_NOT_FOUND` e `REFERENCE_NOT_PROCESSED` no vencimento é o worker: `checkReference` devolve a mesma decisão de esperar para a citada ausente e para a que ainda está em andamento, porque a distinção só importa quando o relógio encerra — e o relógio não está no domínio. `REFERENCE_TTL` e `REFERENCE_INTERVAL` são configuráveis, com 15 minutos e 1 segundo de padrão; valor malformado impede a subida em vez de cair no padrão.

- Operação citada ausente até o prazo: `REJECTED`, `REFERENCE_NOT_FOUND`.
- Operação citada ainda em andamento até o prazo: `REJECTED`, `REFERENCE_NOT_PROCESSED`.
- Operação citada já `REJECTED` ou `FAILED`: `REJECTED` na hora, `REFERENCE_UNSUCCESSFUL`.
- Operação citada `PROCESSED` e compatível: segue a reversão, ou o crédito do `WIN`.

Chegar depois do prazo não reabre um `REJECTED`. `OPENING` vindo de HTTP ou SQS, e carteira inexistente, recusam antes de gravar transação.

## Reversões

`REFUND` reverte um `BET`; `ROLLBACK` reverte um `WIN` ou um `REFUND`. Os dois exigem `referenceExternalTransactionId`, e a reversão usa o valor integral da operação citada — valor diferente rejeita com `REVERSAL_AMOUNT_MISMATCH`.

Cada transação aceita uma única reversão `PROCESSED`, e o índice único parcial no PostgreSQL é a invariante que sustenta isso. Quem **decide** `ALREADY_REVERSED`, porém, é a consulta feita depois do `SELECT … FOR UPDATE` da carteira, e não o índice: ela roda sob o lock, então a segunda reversão só chega a ela quando a primeira já commitou e é visível. É o contrário do que vale para os dois índices de idempotência, cuja busca roda **antes** do lock de propósito — daí o índice ser o árbitro lá e a perdedora reler a linha depois do rollback.

A diferença não é de estilo: `go-db-invariants` exige que a perdedora fique `REJECTED` com `ALREADY_REVERSED`, e a violação de índice aborta a transação SQL inteira, então gravar essa rejeição exigiria uma segunda transação que pode ela mesma falhar e deixar nada gravado. Por isso o nome desse índice não é mapeado para rejeição em `postgres/errors.go`: uma violação dele cai como falha de infraestrutura, a transação desfaz, e o reenvio do provedor encontra a consulta sob o lock respondendo o token corretamente. `ROLLBACK` de um `REFUND` aponta para o estorno, e esse estorno também só aceita um.

A operação citada precisa fechar em jogador, carteira, moeda, rodada e tipo; se não fechar, `REFERENCE_MISMATCH`. Uma reversão que deixaria o saldo negativo rejeita com `REVERSAL_INSUFFICIENT_FUNDS`.

## Erros

O domínio guarda `failureCode` em token estável, e HTTP e SQS devolvem o mesmo token. O catálogo é fechado e enumerável em `internal/domain/wager`, num lugar só, para os canais não divergirem: `INSUFFICIENT_FUNDS`, `REVERSAL_INSUFFICIENT_FUNDS`, `REFERENCE_NOT_FOUND`, `REFERENCE_NOT_PROCESSED`, `REFERENCE_UNSUCCESSFUL`, `ALREADY_REVERSED`, `PLAYER_WALLET_MISMATCH`, `CURRENCY_MISMATCH`, `REVERSAL_AMOUNT_MISMATCH`, `REFERENCE_MISMATCH`, `IDEMPOTENCY_CONFLICT`, `DUPLICATE_EXTERNAL_TRANSACTION`, `WALLET_NOT_FOUND`, `OPENING_NOT_ALLOWED`, `AMOUNT_NOT_ALLOWED_FOR_KIND` e `REFERENCE_REQUIRED`. `Catalog()` devolve a lista que o mapa de status da borda e a série por código percorrem.

Na borda HTTP o corpo de erro segue a RFC 9457, em `application/problem+json`, com `type`, `title`, `status`, `detail` e `instance`. O `failureCode` vai numa extensão: `type` identifica a classe do problema, o token identifica a regra de negócio. O mapa de status numérico fica no adaptador, fora do domínio. Autenticação e entrada inválida também saem como problem details, e entrada inválida não grava transação.

A colisão de unicidade na abertura de carteira fica fora do catálogo: ela recusa um `OpenWallet`, não uma aposta, então sai como 409 com `type` próprio e sem `failureCode`. Acrescentar um token ao catálogo abriria a porta para cada borda inventar o seu, que é o que a lista enumerável existe para impedir.

A falha de infraestrutura passa por `internal/platform/fault`, que captura a stack uma vez, na fronteira onde ela é vista primeiro; o embrulho seguinte só acrescenta a operação. Rejeição de negócio não passa por lá — não há stack para saldo insuficiente. Quem decide o desfecho é quem loga, e loga uma vez.

## Invariantes no banco

O Go pede a escrita; o commit só permanece se o banco aceitar. O agregado não substitui a constraint.

- Carteira: uma linha por jogador e moeda, saldo `BIGINT >= 0`, versão `>= 1`.
- Transação externa: exige provedor, id externo, chave, hash, rodada e jogo, e não pode ser `OPENING`. `OPENING` é interna, sem esses campos, no máximo uma por carteira.
- `LOSS` tem quantia zero; os demais tipos, quantia positiva. `REFUND` e `ROLLBACK` exigem referência. `REJECTED` e `FAILED` exigem `failureCode`. `PROCESSED` exige o saldo observado. `PENDING_REFERENCE` exige `nextAttemptAt`.
- Lançamento: um por transação, quantia positiva, mesma carteira e mesma moeda da transação. `UPDATE`, `DELETE` e `TRUNCATE` são recusados por trigger, e o papel da aplicação só tem `SELECT` e `INSERT` nessa tabela.
- No commit, o saldo da carteira é o saldo posterior do último lançamento. No meio da transação SQL os dois podem divergir, então o gatilho que os compara é `DEFERRABLE INITIALLY DEFERRED`.
- Outbox: `eventId` único, payload imutável depois da inserção — o gatilho recusa o `UPDATE` que o altere —, e publicado e descartado que não coexistem na mesma linha. A varredura da fila de publicação é servida por índice parcial sobre `(wallet_id, created_at, event_id)`, restrito à linha ainda não publicada e não descartada.

O SQL versionado fica em `deploy/migrations` e é aplicado uma vez antes das réplicas — no Compose, por um serviço que termina; no Kubernetes, pelo Job equivalente. O binário da aplicação não carrega código de migration. A migration também cria o papel `wager_app` com esses privilégios, e a aplicação entra com `SET ROLE` em cada conexão do pool, porque um superusuário passaria por cima de qualquer `GRANT`.

## Inbox e outbox

A inbox deduz a reentrega: uma linha por consumidor e `messageId`, com o hash do corpo. A idempotência deduz a operação. As duas entram no mesmo commit do lançamento.

A outbox entra nesse mesmo commit: a abertura de carteira, a submissão nos três desfechos e o encerramento da espera gravam o evento junto do saldo, e o desfazer da transação leva a linha junto. O que um desfecho emite é decidido num lugar só, por uma função do domínio que lê o estado da transação e o movimento da carteira — nenhum caso de uso decide por conta própria que um `LOSS` não emite saldo. Replay e os dois conflitos de idempotência não gravam evento, porque nenhum deles grava transação.

O publisher só lê linha já commitada — nada é publicado antes do commit do saldo. Republicação reutiliza o `eventId`, e o payload não muda.

São quatro eventos, e só estes: `WagerTransactionProcessed` (inclusive `LOSS` e abertura com saldo positivo), `WagerTransactionRejected`, `WalletBalanceChanged` (só quando o saldo mudou) e `WagerTransactionPendingReference`. `FAILED` não tem evento próprio. O envelope leva `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` quando houver, `occurredAt` em UTC e `data`, com `version` 1 fixada pelo construtor. Dinheiro sai como string decimal.

O destino é o tópico SNS FIFO `wallet-events.fifo`: grupo é a carteira, deduplicação é o `eventId`. Por carteira, publica-se só o evento não publicado mais antigo, preservando a ordem; carteiras diferentes não compartilham essa serialização, e a varredura pula com `SKIP LOCKED` a linha que outra réplica segura em vez de esperar por ela.

O turno do relay tem três tempos e duas transações SQL curtas: reivindica a linha gravando token novo e prazo de lease, publica **fora** de qualquer transação, e confirma só se o token ainda for o da reivindicação. O envio é chamada de rede e não pode acontecer com uma transação aberta — uma conexão do pool presa pelo tempo de um broker indisponível esgota o pool sob carga —, e o prazo do envio cabe dentro do lease, para a réplica não estar publicando depois de ter perdido a linha.

A publicação de uma carteira segue a ordem dos commits, que é a sequência que o banco atribui à linha no INSERT — o INSERT acontece sob o lock da carteira. Não é o instante em que o evento aconteceu: esse é lido do relógio do processo antes do lock, e duas operações da mesma carteira podem commitar na ordem inversa dos seus instantes. O lease é medido pelo relógio do banco, não pelo do processo: duas réplicas com relógios diferentes decidiriam de formas diferentes se um lease venceu, e o resultado seria a publicação simultânea da mesma linha.

A linha carrega duas contagens, porque são duas perguntas. A tentativa que não publicou sobe em toda falha e é o que move o backoff: 1s, fator 2, teto 60s. A recusa permanente sobe só quando o broker recusou para valer, e é ela que decide a morte — contar as duas juntas mataria linha sadia, porque um broker fora do ar por dois minutos deixa nove tentativas atrás de si. Rejeição permanente do broker, dez vezes, marca a linha como morta e solta a carteira — a linha permanece, com o mesmo `eventId`, e a linha morta sai da fila junto com a publicada, que é o que faz os eventos seguintes daquela carteira seguirem. A entrega é ao menos uma vez: um lease que vence com o envio em curso pode publicar duas vezes, e quem fecha essa janela é o `eventId` estável, do lado de quem consome.

## Fila de entrada

Filas `wager-transactions.fifo` e `wager-transactions-dlq.fifo`. HTTP e SQS chamam o mesmo caso de uso.

`MessageGroupId` é o id da carteira em minúsculas, o que serializa a mesma carteira e paraleliza carteiras diferentes. `MessageDeduplicationId` é o `messageId` do envelope. A janela de cinco minutos do FIFO não substitui a inbox.

Reentrega do mesmo corpo sai da fila depois do commit. O mesmo `messageId` com outro corpo vai para a DLQ, sem efeito financeiro, e corpo inválido também. Commitado `PROCESSED`, `REJECTED` ou `PENDING_REFERENCE`, a mensagem é apagada. Falha transitória não apaga: backoff de visibilidade com base 1s, fator 2, teto 60s.

O consumidor desiste na quinta entrega e copia para a DLQ. O redrive da fila fica em 15 — mais alto de propósito, para que a mensagem atrás de uma cabeça com falha não seja descartada pelo broker antes da hora.

## Autorização

O Keycloak emite os tokens, com `client_credentials`. Este serviço não cadastra senha e não emite token. A configuração mapeia cada `client_id` a um `providerId` ou ao papel interno de carteira; o mapa local versionado está em `deploy/local/clients.yaml`, e a configuração aponta para ele por caminho.

A borda valida assinatura, emissor e validade contra o JWKS do realm, com chaveiro remoto que trata cache e rotação de chave. O papel vem da claim `azp` cruzada com esse mapa. Um processo sem issuer, sem o caminho do mapa, ou com um mapa ilegível, não abre a porta HTTP: sem mapa ele não sabe autorizar ninguém.

A carteira não tem provedor: dono é o jogador, naquela moeda. O cliente interno abre, lê e reconcilia, e não envia aposta. O provedor envia e lê só a própria transação, inclusive no replay, e não abre carteira nem lê saldo, ledger ou reconciliação.

O `providerId` do corpo ou da URL não autoriza — vale o cliente do token. Corpo de outro provedor, ou leitura de transação alheia, recusa sem movimento e sem revelar se o registro existe. `playerId` diferente do dono da carteira rejeita com `PLAYER_WALLET_MISMATCH`.

Credencial ausente, inválida ou expirada é uma classe; identidade válida sem permissão é outra. As duas saem em `application/problem+json`, sem dado financeiro. `/health/live` e `/health/ready` são públicos.

Na fila, o remetente IAM está mapeado aos provedores que pode enviar (`deploy/local/queue-senders.yaml`). O consumidor confere o `providerId` do corpo contra esse mapa, e remetente sem o provedor vai para a DLQ, sem linha financeira.

## Composição e ciclo de vida

O Uber Fx compõe o processo. A configuração é lida e validada antes de qualquer porta abrir: variável obrigatória ausente derruba a subida, não o primeiro pedido. Os hooks de lifecycle sobem na ordem telemetria, PostgreSQL, fila, tópico, worker de referência, relay da outbox e servidor, e descem na ordem inversa: a porta deixa de aceitar primeiro, e os dois componentes de fundo param de reivindicar depois dela, dentro do mesmo prazo.

Os dois componentes de fundo são runners separados, e não dois usos de um comum. O turno do worker de referência é uma transação SQL; o turno do relay é banco, rede e banco, com um lease no meio. O que os dois compartilham é ticker, lote e parada — perto de vinte linhas —, e uma abstração que cobrisse os dois teria de expor o lease para um e escondê-lo do outro.

No `SIGTERM`: o servidor para de aceitar conexão nova, o pedido em curso termina dentro do prazo, e só depois o buffer de telemetria é descarregado. O worker de referência para de reivindicar espera nova. O publisher não reivindica linha nova e conclui o envio em curso dentro do lease: o sinal cancela a varredura, que nada perde ao ser cortada, e não o envio que já tem reivindicação. Quem corta esse envio é o prazo do processo, e não o sinal. O que ele não confirmar continua publicável, e outra réplica reivindica depois do prazo; a confirmação de um envio que já passou pelo broker não toma esse cancelamento, porque uma confirmação que não aterrissa é uma republicação. O consumidor SQS, quando existir, para de buscar mensagem e devolve com visibilidade zero o que não couber no prazo.

O prazo do processo é `SHUTDOWN_TIMEOUT`, 10 segundos por padrão. O Compose dá 30 segundos antes do `SIGKILL`, e o `terminationGracePeriodSeconds` do Kubernetes cobre o mesmo prazo. Telemetria que não conseguiu sair é registrada no log, e não faz o processo sair com erro.

O domínio não conhece Fx.

## Observabilidade

Log em JSON com lista branca de campos: `correlationId`, `trace_id`, `span_id`, `messageId`, `eventId`, `transactionId`, `walletId`, `providerId`, `kind`, `status`, `failureCode`. O filtro é do handler, não da chamada — atributo fora da lista é descartado antes de chegar à saída, então header de autorização, token, segredo, corpo, quantia e saldo não vazam nem por engano. No HTTP, `X-Correlation-Id` vale se for token opaco curto; senão, usa-se o `trace_id`. No SQS sem correlação, o `messageId`.

Trace por OTLP, com propagação W3C. Um span da entrada, um do caso de uso, um da unit of work fechado no commit; sem span por query. `REJECTED` deixa o span em ok — erro de span é falha de infraestrutura, conflito de versão ou DLQ. A correlação é decidida uma vez na borda, antes do handler, e viaja no contexto: o que a operação grava a carrega, e a linha de acesso no fim seria tarde demais. A outbox grava `trace_id` e `span_id` do commit, e o publisher abre span próprio ligado a eles por *link*, e não por paternidade — aquele span fechou no commit, e depois de um restart nem existe mais no processo. Uma linha que voltou para nova tentativa deixa o span em `ok`: broker fora é para voltar, e um span em erro por tentativa faria uma indisponibilidade parecer defeito.

`/metrics` é Prometheus, com exemplar de `trace_id` na latência, sem `walletId` nem `providerId` no rótulo. A métrica de processo sai só por esse caminho, não por OTLP, para heap e goroutines terem uma fonte única. `/health/live`, `/health/ready` e `/metrics` ficam fora do span e do log, porque a sonda e o scrape batem de segundo em segundo. O `pprof` escuta fora da porta da API.

`/health/live` responde pelo processo; `/health/ready` consulta PostgreSQL e SQS com prazo curto, e ready falho não derruba o live.

## Limitações conhecidas

- O LocalStack community não persiste: qualquer reinício esvazia filas e tópico, e é preciso rodar `terraform apply` de novo. O IAM dele também é parcial — o principal é criado, mas a política pode não ser aplicada como na AWS.
- O Compose sobe uma réplica do processo. As três instâncias independentes que o desafio pede são reproduzidas com Kind ou k3d, com a migration como Job que roda uma vez antes das réplicas.
- O realm de teste não tem mapper de audience, e os clientes estão com `fullScopeAllowed`. A borda não confere `aud`: o token de `client_credentials` não carrega audiência deste serviço. Quem decide é o cliente do token, na claim `azp`, cruzado com o mapa versionado.
- O piso de cobertura hoje é 70% em `internal/platform` e 80% em `internal/app`, com margem estreita. Os caminhos de I/O de `postgres`, `probe` e `telemetry` são cobertos pela suíte de integração, que não entra nesse cálculo.
- O guia separado de ambiente de teste, execução em múltiplas instâncias e simulação de falha ainda não existe. As instruções de subida e de teste estão no `README.md`.
- O tópico `wallet-events.fifo` é provisionado sem subscription, de propósito: não há consumidor dos eventos ainda. A suíte de jornada anexa um assinante só pelo tempo do caso, porque sem um não haveria como afirmar o que chegou ao tópico.
- O `causationId` do envelope sai omitido nesta entrega. A operação chega por HTTP, cujo cabeçalho de correlação vira `correlationId`, e o worker de referência age por prazo, não por mensagem. Quem preenche o campo é a entrada por SQS, com o `messageId` da mensagem que causou a operação.
- Métrica e painel do atraso da outbox ainda não existem. A linha morta fica visível no log e no banco, não silenciosa, mas nenhum alarme a observa.
- O endereço do JWKS é configurado à parte do issuer (`IDP_JWKS_URL`), porque no Compose o token anuncia `localhost` e o processo precisa buscar a chave em `keycloak`. Vazio, ele é derivado do issuer. A descoberta OIDC não é feita na subida, para o IdP não entrar no caminho de boot.
