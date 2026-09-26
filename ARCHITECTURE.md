# Arquitetura

Serviço de liquidação de apostas: um bounded context, um binário, várias réplicas. Provedores de jogo enviam transações por HTTP e por fila; o serviço debita e credita carteiras, grava um ledger imutável e publica eventos.

Este arquivo é o índice. A estrutura está em `docs/`, com diagramas; o porquê de cada escolha está em `docs/adr/`, um arquivo por decisão; como rodar está no `README.md`.

## Como ler

| Quero saber | Onde |
| --- | --- |
| quem fala com o sistema, do que ele é feito, o que configura | [01 · Contexto e containers](docs/01-contexto.md) |
| os agregados, os seis tipos de operação, o ciclo de vida | [02 · Domínio](docs/02-dominio.md) |
| as cinco tabelas, as invariantes, as migrations | [03 · Dados](docs/03-dados.md) |
| o que acontece, passo a passo, numa aposta, num replay, numa corrida, numa espera | [04 · Fluxos](docs/04-fluxos.md) |
| como um erro viaja, quem pode o quê, o que sai em log, como o processo sobe e desce | [05 · Transversais](docs/05-transversais.md) |
| o que falta, o que é guarda, o que custa | [06 · Riscos e limitações](docs/06-riscos-e-limitacoes.md) |
| por que foi feito assim, e o que foi rejeitado | [Decisões](docs/adr/README.md) |
| subir, testar, chamar as rotas | [README](README.md) |

Cada tipo de conteúdo tem um lugar só. Estrutura muda com o código e mora em `docs/0X`. Uma decisão nunca é editada: quando muda, outra a substitui, e a antiga fica. Um comentário de código que precise de mais de três linhas de porquê aponta para o ADR pelo número.

## Estratégia

**Duas raízes ligadas por identidade.** `Wallet` guarda saldo e versão; `WagerTransaction` guarda o pedido do provedor e o que aconteceu com ele, e referencia a carteira pelo id. `LedgerEntry` nasce dentro do movimento da carteira e só é inserido. O domínio importa a biblioteca padrão e a si mesmo; Fx, `pgx`, HTTP e SQS ficam nos adaptadores.

**O banco é o árbitro.** O que duas réplicas poderiam decidir diferente é decidido por índice único, por lock de linha ou por `CHECK`: a idempotência pelos dois índices, o saldo pelo `SELECT … FOR UPDATE` mais a versão no `UPDATE`, a máquina de estados pelos `CHECK` de status. O agregado afirma as mesmas invariantes, e o banco tem a palavra final.

**Um commit.** A inbox da mensagem, o saldo, a linha da transação, o lançamento e os eventos da outbox entram na mesma transação SQL. Uma rejeição de negócio commita a própria linha `REJECTED` e ainda assim sai como `422`. Falha transitória desfaz tudo e tenta de novo; nada é publicado antes do commit.

**Duas classes de erro, que não se misturam.** Rejeição de negócio carrega um token do catálogo fechado, deixa o span ok e não tem stack. Falha de infraestrutura tem stack capturada uma vez, marca o span e não tem token. A borda separa as duas com `errors.As`, e o mapa de status HTTP mora no adaptador.

**Três camadas, uma direção.** `internal/platform` implementa e injeta; `internal/app` coordena e declara as portas que precisa; `internal/domain` decide. A seta nunca aponta para cima: um caso de uso nunca nomeia `pgx`, e um teste de unidade nunca precisa de banco.

## Estado da implementação

| Área | Estado |
| --- | --- |
| Ambiente: Compose, Terraform local, realm do Keycloak | implementado |
| Processo: Fx, configuração, health, log JSON, OTLP, `/metrics`, `pprof` | implementado |
| Domínio: identidades, `Money`, `Wallet`, `LedgerEntry`, `WagerTransaction`, eventos | implementado |
| Schema das cinco tabelas, constraints, migration aplicada antes das réplicas | implementado |
| Pool `pgx`, unit of work em `READ COMMITTED`, repositórios | implementado |
| Borda HTTP com `application/problem+json` | implementado |
| Autorização por token nas rotas de carteira e de aposta | implementado |
| Idempotência persistente: chave, hash canônico, replay, os dois conflitos pelo índice | implementado |
| Lock pessimista com guarda de versão | implementado |
| `BET`, `LOSS`, `WIN`, `REFUND`, `ROLLBACK`, `PENDING_REFERENCE` com `202`, TTL e worker | implementado |
| Outbox no commit, relay com lease por carteira, publicação no tópico FIFO | implementado |
| Inbox no commit, consumidor da fila FIFO, backoff de visibilidade, DLQ | implementado |
| Ledger paginado, reconciliação | decidido, não implementado |

## Decisões, por tema

Vinte, em [`docs/adr/`](docs/adr/README.md). Agrupadas:

- **Concorrência e idempotência** — [0001](docs/adr/0001-indice-unico-como-arbitro-da-idempotencia.md) índice único como árbitro · [0002](docs/adr/0002-lock-pessimista-com-guarda-de-versao.md) lock pessimista com guarda de versão · [0003](docs/adr/0003-already-reversed-decidido-sob-o-lock.md) `ALREADY_REVERSED` sob o lock · [0016](docs/adr/0016-inbox-em-savepoint.md) inbox em savepoint
- **Modelo e contrato** — [0004](docs/adr/0004-rejeicao-duravel-ao-lado-do-resultado.md) rejeição durável ao lado do resultado · [0005](docs/adr/0005-moeda-denormalizada-na-transacao-e-no-lancamento.md) moeda gravada na transação e no lançamento · [0006](docs/adr/0006-202-para-pending-reference.md) `202` para a espera · [0007](docs/adr/0007-catalogo-fechado-de-failure-code.md) catálogo fechado de `failureCode` · [0009](docs/adr/0009-raw-message-na-referencia-opcional.md) `json.RawMessage` na referência
- **Erros** — [0008](docs/adr/0008-interfaces-de-comportamento-no-consumidor.md) interfaces de comportamento no consumidor · [0010](docs/adr/0010-stack-capturada-uma-vez.md) stack capturada uma vez
- **Processo** — [0011](docs/adr/0011-tres-runners-de-fundo-separados.md) três runners separados · [0012](docs/adr/0012-flush-de-telemetria-fora-do-lifecycle.md) flush fora do lifecycle · [0020](docs/adr/0020-jwks-separado-do-issuer.md) JWKS separado do issuer
- **Outbox** — [0013](docs/adr/0013-publicar-fora-da-transacao-sob-lease.md) publicar fora da transação · [0014](docs/adr/0014-tempo-do-banco-na-outbox.md) tempo do banco · [0015](docs/adr/0015-duas-contagens-na-outbox.md) duas contagens
- **Fila de entrada** — [0017](docs/adr/0017-invisibilidade-cobre-long-poll-e-processamento.md) invisibilidade cobre long poll e processamento · [0018](docs/adr/0018-desistencia-na-quinta-entrega-redrive-em-quinze.md) desistência na quinta entrega · [0019](docs/adr/0019-mapa-de-remetentes-pela-identidade-observada.md) mapa pela identidade observada
