# 0029. O contrato HTTP segue o enunciado

Status: aceita · 2026-09-26

## Contexto

O contrato HTTP nasceu das regras de domínio, e o enunciado do desafio não mora no repositório. Comparados, os dois divergiam em três pontos sem registro: a submissão respondia `id` e `observedBalance` e omitia `idempotentReplay` quando falso, onde o enunciado mostra `transactionId`, `balance` e `idempotentReplay: false`; a reconciliação era `GET` com `ledgerBalance` e `entryCount`, onde o enunciado pede `POST` com `calculatedBalance`, `difference` e `checkedEntries`; e a consulta `GET /providers/:providerId/wagering/transactions/:externalTransactionId` não existia. Quem avalia roda o exemplo do enunciado e lê o JSON pelo nome. A rejeição tem um conflito próprio: o exemplo de resposta usa `status` para o estado da transação, e a rejeição de regra sai em problem details, onde `status` é o código HTTP.

## Opções consideradas

1. Manter o contrato próprio e documentar cada divergência.
2. Seguir o enunciado em tudo, inclusive responder a rejeição de regra com o corpo da submissão e `status: "REJECTED"`, fora de problem details.
3. Seguir o enunciado nos nomes, no verbo e na rota, manter como extensão os campos que ele não mostra, e manter a rejeição em problem details com a extensão `transactionId`.

## Decisão

Opção 3. A opção 1 não muda o que o exemplo recebe: um campo com outro nome é um campo ausente para quem lê pelo nome, e uma rota que não existe responde 404, com ou sem documento ao lado. A opção 2 quebra a forma única da recusa na borda, o RFC 9457, onde `status` é o número HTTP: o mesmo nome não pode ser também o estado da transação sem que um cliente de problem details leia um no lugar do outro. Ela ainda apagaria a separação entre o 201/200 de uma conclusão e o 422 de uma recusa, que é como o provedor sabe se dinheiro se moveu sem ler o corpo.

A rejeição que gravou linha passa a trazer `transactionId`, na primeira recusa e no replay dela, e o estado `REJECTED` fica legível pela consulta com essa identidade. A recusa que não gravou linha sai sem ele. É a única divergência deliberada do enunciado.

Tipo, identificador externo, quantia, provedor e `failureCode` continuam ao lado dos nomes do enunciado: quem lê pelo nome os ignora, e quem opera precisa deles. A submissão e a consulta deixam de dividir a resposta, porque só a submissão é uma chegada da operação; na consulta, `idempotentReplay: false` diria algo falso sobre uma leitura.

A reconciliação passa a `POST` sem mudar o efeito: não lê corpo, não trava, não escreve, e o mesmo estado dá a mesma resposta. A diferença é decidida junto com o veredito, pela subtração de `Money`, e não na borda, para a rota e o observador de divergência continuarem dividindo uma coisa só. O `GET` no mesmo caminho cai na rota inexistente e responde 404, e não 405: um 405 com `Allow` exigiria tirar do `ServeMux` o `/` que dá span e log a toda rota desconhecida.

## Consequências

- O exemplo do enunciado encontra os nomes que procura: `transactionId`, `balance` e `idempotentReplay` sempre presente na submissão; `calculatedBalance`, `difference` com sinal e `checkedEntries` na reconciliação; e a consulta do provedor pelo identificador externo.
- A consulta por identificador externo leva o provedor do token para a query, como a consulta por identidade. O `providerId` da URL não autoriza: o de outro provedor responde a mesma ausência, antes de qualquer consulta.
- A fila aceita o envelope do exemplo como está escrito: `type` e `occurredAt` são ignorados, não recusados, e ficam fora do hash — o que antes era acaso do decode passou a ter teste.
- **O que se paga:** o contrato quebrou para quem lia os nomes antigos, que era só a suíte, atualizada no mesmo recorte. A rejeição continua não sendo o corpo do exemplo, e um cliente que espere `status: "REJECTED"` na resposta da submissão precisa ler a consulta. E um `POST` de leitura é um verbo que um intermediário não trata como seguro; aqui repeti-lo não muda nada.

## Onde está no código

- `internal/platform/wagerapi/transactions.go` — `submittedResponse` e `recordedResponse`, as duas respostas; `ReadByExternal` e `ownedExternal`, a consulta pelo identificador externo.
- `internal/platform/wagerapi/reporter.go` — `rowOf`, que preenche `transactionId` na rejeição que gravou linha.
- `internal/platform/problem/problem.go` — `Details.TransactionID`, a extensão omitida quando vazia.
- `internal/app/reconcilewallet/reconcilewallet.go` — `Report.Difference`, decidida com o veredito.
- `internal/platform/walletapi/reconciliation.go` — `reconciliationResponse` e `Reconcile`, com os nomes do enunciado.
- `internal/platform/httpapi/access.go` — `Handler`: o `POST` da reconciliação e o padrão da consulta por provedor.
- `internal/platform/wagerqueue/decode.go` — `envelope`, que não lê `type` nem `occurredAt`.
