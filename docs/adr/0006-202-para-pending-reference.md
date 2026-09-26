# 0006. `202 Accepted` para a operação que entra em espera

Status: aceita · 2026-09-26

## Contexto

Uma operação que cita outra ainda não chegada entra em `PENDING_REFERENCE`: a linha é gravada, nada se move, e o worker fecha depois. A borda HTTP tem de responder isso, e a primeira conclusão de uma operação responde `201 Created`.

## Opções consideradas

1. `201 Created` com `"status": "PENDING_REFERENCE"` no corpo.
2. `200 OK`.
3. `202 Accepted`, com `Location` apontando o recurso e o estado no corpo, sem saldo observado.

## Decisão

Opção 3. O código descreve o request, não a linha: a primeira conclusão **criou e decidiu**; a espera **criou e não decidiu**; o replay **encontrou**.

Empilhar a espera no `201` obrigaria o provedor a ler o corpo para saber se houve movimento financeiro — que é exatamente o que um código distinto evita. `200` diria que nada foi criado, e foi.

## Consequências

- Três códigos de sucesso na rota: `201` decidida, `202` aceita e esperando, `200` replay de qualquer uma das duas.
- `Location` sai nos dois primeiros, porque nos dois há recurso novo.
- A espera não sai em problem details: não é recusa. E o replay de uma espera responde a espera gravada, marcada como replay e sem saldo observado — nenhum commit a fechou, então não há saldo.

## Onde está no código

- `internal/platform/wagerapi/transactions.go` — `answer`, `statusOf`.
- `internal/app/submitwager/submitwager.go` — `waitingOf`.
