# Fluxos

As sequências que o texto não mostra bem: quem chama quem, em que ordem, e onde a transação SQL abre e fecha. Cada diagrama é um caminho real do código; o nome das funções é o do fonte.

## 1. Uma aposta, do socket ao `201`

```mermaid
sequenceDiagram
    autonumber
    participant P as Provedor
    participant B as Borda HTTP<br/>httpapi · authz · wagerapi
    participant U as submitwager
    participant D as Domínio<br/>wager · wallet · ledger
    participant PG as PostgreSQL

    P->>B: POST /wagering/transactions<br/>Bearer · Idempotency-Key · corpo
    B->>B: valida assinatura (JWKS), lê azp,<br/>resolve cliente pelo mapa, exige papel Provider
    B->>B: decodeSubmit: parse de cada campo,<br/>speaksFor: provedor do token == corpo
    B->>U: Submit(Command)
    U->>U: pending: cunha 4 ids, lê o relógio,<br/>hash do negócio, NewExternal → PENDING em memória
    U->>PG: BEGIN READ COMMITTED
    U->>PG: SELECT por (provider, key) — sem lock
    PG-->>U: nada: primeira chegada
    U->>PG: SELECT wallet FOR UPDATE
    PG-->>U: estado (saldo, versão lida)
    U->>D: wallet.Rehydrate · wager.Bet → Debit → move
    D-->>U: Decision: Entry, saldo novo, versão+1
    U->>D: Transaction.Process(saldo observado)
    U->>PG: UPDATE wallets … WHERE version = lida
    U->>PG: INSERT wager_transactions (PROCESSED)
    U->>PG: INSERT ledger_entries
    U->>PG: INSERT outbox_events ×2<br/>Processed · BalanceChanged
    U->>PG: COMMIT
    U-->>B: Result
    B->>B: Reporter.Settled: 3 atributos no span,<br/>1 linha de log
    B-->>P: 201 Created · Location · corpo
```

A ordem dentro da transação é fixa: chave, carteira, citada, decisão, escrita. É essa ordem que separa uma recusa sem linha de uma rejeição que grava.

## 2. Replay e conflito pelo caminho rápido

A mesma chave chegando depois — retry do provedor por timeout, por exemplo — nunca chega ao lock.

```mermaid
sequenceDiagram
    autonumber
    participant P as Provedor
    participant U as submitwager
    participant PG as PostgreSQL

    P->>U: mesma Idempotency-Key
    U->>PG: BEGIN
    U->>PG: SELECT por (provider, key)
    PG-->>U: linha gravada: body_hash, status, saldo observado
    alt hash igual
        U->>U: Rehydrate → Replay() → Outcome
        U->>PG: COMMIT (nada escrito)
        U-->>P: 200 · idempotentReplay: true<br/>saldo observado da época, não o atual
    else hash diferente
        U->>U: settlement{rejection: IDEMPOTENCY_CONFLICT}
        U->>PG: COMMIT (nada escrito)
        U-->>P: 422 · failureCode IDEMPOTENCY_CONFLICT
    end
```

Esta leitura não é o árbitro — é otimização do caso comum. Duas réplicas podem ambas não achar nada; quem decide então é o índice ([ADR 0001](adr/0001-indice-unico-como-arbitro-da-idempotencia.md)).

## 3. A corrida entre réplicas

```mermaid
sequenceDiagram
    autonumber
    participant A as Réplica A
    participant B as Réplica B
    participant PG as PostgreSQL

    par
        A->>PG: BEGIN · SELECT por chave → nada
    and
        B->>PG: BEGIN · SELECT por chave → nada
    end
    A->>PG: SELECT wallet FOR UPDATE
    B->>PG: SELECT wallet FOR UPDATE
    Note over B,PG: B bloqueia no lock
    A->>PG: INSERT wager_transactions
    A->>PG: COMMIT
    Note over B,PG: lock liberado, B lê o saldo já commitado
    B->>PG: INSERT wager_transactions
    PG-->>B: 23505 · wager_transactions_one_per_provider_and_key
    B->>PG: ROLLBACK (o débito de B morre aqui)
    B->>PG: SELECT por chave — do pool, fora da transação abortada
    PG-->>B: a linha de A
    B->>B: outcomeOf: mesmo hash → replay · outro hash → conflito
```

`afterRace` relê **fora** da transação porque, depois de um erro, o PostgreSQL recusa todo comando até o `ROLLBACK`. Uma chegada gêmea viola os dois índices e o banco nomeia só o primeiro — por isso quem responde é a linha vencedora, não o nome do índice.

## 4. Uma rejeição que deixa linha

```mermaid
sequenceDiagram
    autonumber
    participant B as Borda
    participant U as submitwager
    participant D as Domínio
    participant PG as PostgreSQL

    B->>U: Submit(BET 80.00 com saldo 20.00)
    U->>PG: BEGIN · SELECT wallet FOR UPDATE
    U->>D: wager.Bet → Debit → nextBalance
    D-->>U: wallet.ErrInsufficientFunds
    Note over D: translate embrulha:<br/>Rejection{INSUFFICIENT_FUNDS, cause}
    U->>U: reject: errors.As Rejection? sim
    U->>D: Transaction.Reject(INSUFFICIENT_FUNDS)
    U->>PG: INSERT wager_transactions (REJECTED, failure_code)
    U->>PG: INSERT outbox_events (Rejected)
    Note over U,PG: a função de trabalho devolve nil:<br/>a rejeição viaja no campo, não como erro
    U->>PG: COMMIT
    U->>U: answerOf → (Result, rejection)
    U-->>B: erro: submit wager: wager: rejected with INSUFFICIENT_FUNDS
    B->>B: problem.From → 422 + token · span ok · sem stack
```

Sem versão nova, sem lançamento, sem `WalletBalanceChanged`. A linha `REJECTED` é resultado registrado e consultável ([ADR 0004](adr/0004-rejeicao-duravel-ao-lado-do-resultado.md)).

## 5. A espera por referência, e quem a fecha

Um `WIN` que cita uma aposta ainda não chegada pelo outro canal.

```mermaid
sequenceDiagram
    autonumber
    participant P as Provedor
    participant U as submitwager
    participant D as Domínio
    participant PG as PostgreSQL
    participant W as referenceworker<br/>+ resolvereference

    P->>U: WIN citando bet-99
    U->>PG: BEGIN · lock da carteira
    U->>PG: SELECT por (provider, external_id = bet-99)
    PG-->>U: não existe
    U->>D: wager.Win(ref.Cited = nil) → checkReference
    D-->>U: Decision{waiting}
    U->>D: WaitForReference(nextAttemptAt, deadline = agora + TTL)
    U->>PG: INSERT wager_transactions (PENDING_REFERENCE)
    U->>PG: INSERT outbox_events (PendingReference)
    U->>PG: COMMIT
    U-->>P: 202 Accepted · Location

    loop a cada REFERENCE_INTERVAL
        W->>PG: DueWaits(agora) — sem lock, sem transação
        PG-->>W: candidatos (id, wallet_id)
        W->>PG: BEGIN · SELECT wallet FOR UPDATE
        W->>PG: ClaimWait: FOR UPDATE SKIP LOCKED<br/>WHERE next_attempt_at <= agora
        alt outra réplica já segura a linha
            PG-->>W: nenhuma linha: pula
        else a linha é desta réplica
            W->>PG: SELECT citada por external_id
            alt citada PROCESSED e fecha
                W->>D: wager.Win → Credit
                W->>PG: UPDATE wallet · EndWait(PROCESSED) · INSERT entry · outbox
            else citada ausente ou ainda em curso, antes do prazo
                W->>PG: RescheduleWait(backoff 1s ×2 até 60s, nunca além do prazo)
            else prazo vencido
                W->>PG: EndWait(REJECTED, REFERENCE_NOT_FOUND ou REFERENCE_NOT_PROCESSED)
            else citada REJECTED/FAILED, ou não fecha
                W->>PG: EndWait(REJECTED, REFERENCE_UNSUCCESSFUL ou REFERENCE_MISMATCH)
            end
            W->>PG: COMMIT
        end
    end
```

A varredura escolhe candidatos sem lock; a decisão relê a linha **sob** o lock da carteira e é esse estado que vale. A ordem carteira → transação é a mesma da submissão, e é o que impede o worker e uma aposta de se abraçarem ([ADR 0002](adr/0002-lock-pessimista-com-guarda-de-versao.md)).

## 6. O turno do relay da outbox

```mermaid
sequenceDiagram
    autonumber
    participant R as outboxrelay<br/>+ relayoutbox
    participant PG as PostgreSQL
    participant SNS as SNS FIFO

    loop a cada OUTBOX_INTERVAL
        R->>PG: Due: a linha não publicada mais antiga por carteira,<br/>SKIP LOCKED nas que outra réplica segura
        PG-->>R: candidatos
        R->>PG: BEGIN · reivindica: lease_token novo,<br/>lease_until = now() + OUTBOX_LEASE · COMMIT
        R->>SNS: Publish — fora de qualquer transação<br/>MessageGroupId = carteira · DeduplicationId = event_id
        alt publicado
            R->>PG: BEGIN · UPDATE published_at<br/>WHERE lease_token = o da reivindicação · COMMIT
        else falha transitória
            R->>PG: attempt_count + 1 · next_attempt_at por backoff
        else recusa permanente do broker
            R->>PG: refusal_count + 1
            opt décima recusa
                R->>PG: dead_at = now(): a carteira segue
            end
        end
    end
```

A publicação fica fora da transação para uma conexão do pool não ficar presa pelo tempo de um broker lento ([ADR 0013](adr/0013-publicar-fora-da-transacao-sob-lease.md)). O lease é medido pelo relógio do banco, e a ordem por carteira é a do `INSERT` sob o lock, não a do instante do evento ([ADR 0014](adr/0014-tempo-do-banco-na-outbox.md)).

## 7. Uma mensagem da fila, do long poll ao `Delete`

```mermaid
sequenceDiagram
    autonumber
    participant SQS as SQS FIFO
    participant C as wagerqueue<br/>+ receivewager
    participant U as submitwager
    participant PG as PostgreSQL
    participant DLQ as DLQ

    loop long poll de QUEUE_POLL
        C->>SQS: Receive(visibilidade = QUEUE_VISIBILITY)
        SQS-->>C: entregas (sender, messageId, corpo, contagem)
    end
    alt corpo inválido, ou remetente não mapeado, ou provedor fora da lista
        C->>DLQ: copia com a razão
        C->>SQS: Delete
    else quinta entrega
        C->>DLQ: copia
        C->>SQS: Delete
    else
        C->>U: SubmitCaused(Command, Caused{consumer, messageId, hash})
        U->>PG: BEGIN
        U->>PG: SAVEPOINT · INSERT inbox_messages
        alt já registrada, mesmo hash
            Note over U,PG: reentrega legítima: nada é reaplicado,<br/>o SELECT por chave logo abaixo responde
        else já registrada, outro hash
            U-->>C: ErrMessageBodyDiffers
            C->>DLQ: copia
            C->>SQS: Delete
        end
        U->>PG: … o mesmo caminho da aposta por HTTP …
        U->>PG: COMMIT
        alt PROCESSED, REJECTED ou PENDING_REFERENCE
            C->>SQS: Delete
        else falha transitória
            C->>SQS: devolve com visibilidade por backoff (1s ×2 até 60s)
        end
    end
```

HTTP e fila chamam o mesmo caso de uso; o que a fila acrescenta é o par que o HTTP não tem — a identidade do remetente e a memória da mensagem — e a inbox entra no mesmo commit do lançamento ([ADR 0016](adr/0016-inbox-em-savepoint.md)). No `SIGTERM`, a busca é cancelada e a decisão em curso não; a mensagem cortada pelo prazo volta com visibilidade zero.

## 8. As duas leituras da carteira

```mermaid
sequenceDiagram
    autonumber
    participant I as Cliente interno
    participant B as walletapi
    participant R as reconcilewallet
    participant PG as PostgreSQL

    I->>B: GET /wallets/{walletId}/reconciliation
    B->>R: Reconcile(walletId)
    R->>PG: uma SELECT: wallets ⋈ (soma, contagem, última sequência, primeira quebra) do ledger
    Note over R,PG: sem transação, sem FOR UPDATE:<br/>uma sentença é um snapshot, e não espera a aposta em curso
    PG-->>R: LedgerSummary
    R->>R: divergencesOf: BALANCE_MISMATCH · SEQUENCE_GAP · CHAIN_BREAK
    R-->>B: Report
    alt consistent
        B-->>I: 200 sem divergences
    else divergiu
        B->>B: Reporter.Diverged: log com walletId e os tokens, span ok
        B-->>I: 200 com divergences e firstBreakSequence
    end
```

O extrato segue o mesmo molde com duas idas ao banco — existe a carteira; a página keyset de `limit + 1` linhas sobre `(sequence_number, id)` — porque nada apaga carteira, e a linha excedente é o que diz que há próxima página sem contar a tabela. O cursor decodifica antes de qualquer consulta, e é recusado se foi emitido para outra carteira ([ADR 0023](adr/0023-cursor-opaco-amarrado-a-carteira.md)). A reconciliação lê tudo numa sentença e decide fora dela: o SQL devolve números, e o veredito é regra testável sem banco, que o observador de divergência chama sem passar pela rota ([ADR 0022](adr/0022-reconciliacao-em-uma-sentenca-com-veredito-no-caso-de-uso.md), [ADR 0024](adr/0024-observador-de-divergencia-por-cursor-em-memoria.md)). Nenhuma das duas escreve, e a divergência não marca o span: é um resultado que a leitura relata, não uma falha do serviço.
