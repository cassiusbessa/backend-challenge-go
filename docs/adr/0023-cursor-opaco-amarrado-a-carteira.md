# 0023. Cursor opaco amarrado à carteira

Status: aceita · 2026-09-26

## Contexto

O extrato é paginado por keyset sobre `(sequence_number, id)`, a ordem que o ledger já fixa, e a página seguinte precisa saber de onde continuar. Esse "de onde" viaja no cliente entre duas requisições, e a rota é chamada com a carteira na URL: o token pode ou não repetir essa carteira, e pode ou não ser verificável. O cliente é interno, então o risco não é vazamento — é a rota responder errado sem avisar.

## Opções consideradas

1. Cursor com só a posição `(sequence, id)`, em `base64url`.
2. Cursor com `walletId:sequence:id`, em `base64url` sem padding, com o decodificador exigindo que a carteira seja a da URL.
3. Cursor assinado com HMAC, para que só o servidor produza um válido.
4. Paginação por `offset`.

## Decisão

Opção 2. Um cursor da carteira A apresentado na carteira B é recusado com o **mesmo** erro de um cursor malformado, então o corpo da recusa não diz nada sobre A e B nunca responde uma página a partir de uma posição que não lhe pertence.

Com a opção 1, o mesmo cursor responderia em silêncio com as linhas de B a partir daquela posição: uma resposta certa para uma pergunta que ninguém fez, indistinguível de uma página legítima. A opção 3 traz um segredo para gerir e rotacionar, e não compra nada: o cursor não carrega nada secreto, e o único uso indevido — a carteira errada — a amarração já fecha; um cursor forjado com a carteira certa e uma posição inventada devolve a página a partir dali, que é o que `limit` e `cursor` honestos também fariam. A opção 4 repete e pula lançamentos quando um é gravado entre duas páginas, e a suíte de jornada afirma justamente o contrário.

O `id` fica no cursor, na comparação de linha e no `ORDER BY` mesmo que, dentro de uma carteira, `UNIQUE (wallet_id, sequence_number)` nunca o deixe decidir: a ordem do extrato é o par que a coleção do domínio e a regra de leituras já fixam, e o custo de carregá-lo é zero. O comentário ao lado diz por que ele é inerte, para ninguém o remover como simplificação nem o ler como sinal de empate possível.

O codec mora no caso de uso, `listledger`, e não na borda: o cursor é o token de paginação da aplicação. A borda passa a string e traduz a recusa em `400` nomeando o campo `cursor`, nunca o valor.

## Consequências

- Toda recusa de cursor — malformado, sequência abaixo de 1, identificador fora de forma, carteira errada — é um único sentinela, `ErrInvalidCursor`, e um único corpo.
- O cliente não precisa interpretar o token para continuar, e nenhuma recusa devolve o conteúdo dele.
- `limit` fora de 1 a 200 é recusado e não clampado, pelo mesmo princípio de `money.Parse`: o cliente pediu o que a rota não serve, e a resposta certa é dizer isso.
- **O que se paga:** o cursor cresce 37 bytes com a carteira que a URL já diz, e o extrato faz duas idas ao banco — existe a carteira; a página — porque a alternativa de uma sentença com `LEFT JOIN LATERAL` serviria o 404 com um scan de colunas todas anuláveis. Duas idas são seguras aqui porque nada apaga carteira; se um dia apagar, o `LATERAL` é o caminho.

## Onde está no código

- `internal/app/listledger/cursor.go` — `encodeCursor`, `decodeCursor`, `issuedFor`, `ErrInvalidCursor`.
- `internal/app/listledger/listledger.go` — `Service.Page`, o `limit + 1` e o corte; `DefaultLimit`, `MaxLimit`.
- `internal/platform/postgres/ledger.go` — `selectEntriesAfter`, a comparação de linha e o `ORDER BY` com o `id` inerte; `existsWallet`.
- `internal/platform/walletapi/decode.go` — `refusalOf`, a tradução em `invalidField{name: "cursor"}`.
- `internal/e2e/wallet/ledger_test.go` — `TestListLedger_refusesTheCursorOfAnotherWalletTheSameWayAsAMalformedOne`, `TestListLedger_showsAMovementSettledBetweenTwoPagesOnTheNextOne`.
