# 0016. A inbox é gravada num savepoint, dentro do commit da operação

Status: aceita · 2026-09-26

## Contexto

O consumidor da fila precisa deduzir a reentrega de uma mensagem — o mesmo `messageId` chegando de novo — sem reaplicar a operação, e distinguir a reentrega legítima (mesmo corpo) de um `messageId` reutilizado com outro corpo. Duas réplicas podem receber a mesma mensagem ao mesmo tempo.

## Opções consideradas

1. `SELECT` na inbox antes de processar; se existe, comparar o hash.
2. Gravar a inbox numa transação própria, antes da operação.
3. Tentar o `INSERT` na inbox dentro do commit da operação, num savepoint, e ler a violação de unicidade como a resposta.

## Decisão

Opção 3. A opção 1 tem a mesma janela da idempotência ([ADR 0001](0001-indice-unico-como-arbitro-da-idempotencia.md)): duas réplicas passam. A opção 2 separa a memória da mensagem do saldo que ela moveu — uma falha entre as duas transações deixa a mensagem "vista" sem efeito, e a reentrega é descartada com o dinheiro nunca movido.

O savepoint existe porque a violação de unicidade abortaria o resto da transação, e não sobraria com que comparar o hash.

## Consequências

- Inbox e lançamento vivem ou morrem juntos.
- Hash igual é reentrega legítima: nada é reaplicado, e quem responde o desfecho é a transação já gravada sob a chave de idempotência, que a leitura logo depois encontra.
- Hash diferente é `ErrMessageBodyDiffers`: nem regra recusando nem falha transitória. A mensagem vai para a DLQ sem gravar nada.
- A inbox roda **primeiro** no `decide`, antes de qualquer lock: uma reentrega não chega à carteira.
- A inserção da inbox e a da idempotência não se substituem: a primeira não pega o mesmo negócio em duas mensagens com identificadores diferentes; a segunda não pega o mesmo identificador reentregue.

## Onde está no código

- `internal/app/submitwager/submitwager.go` — `receive`, `Caused`, `ErrMessageBodyDiffers`.
- `internal/platform/postgres/inbox.go` — o `INSERT` sob savepoint.
- `deploy/migrations/000006_wager_inbox.up.sql`.
