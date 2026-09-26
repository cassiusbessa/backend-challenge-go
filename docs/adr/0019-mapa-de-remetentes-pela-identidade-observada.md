# 0019. O mapa de remetentes da fila é chaveado pela identidade que o consumidor observa

Status: aceita · 2026-09-26

## Contexto

Na fila não há token: a identidade do remetente vem do broker, junto da mensagem. O consumidor precisa conferir se aquele remetente pode enviar pelo `providerId` que o corpo declara. O Terraform cria um principal IAM por remetente, com nome próprio.

## Opções consideradas

1. Mapa chaveado pelo nome do principal IAM.
2. Mapa chaveado pela identidade que chega **na mensagem**, como string opaca, com a lista de provedores permitidos por entrada.

## Decisão

Opção 2. O nome do principal não chega ao consumidor — um mapa por ele não poderia ser conferido contra mensagem nenhuma.

O valor é opaco: o código não o interpreta, não valida formato e não deriva nada dele. Em produção é o identificador do principal remetente; no broker local, medido, é o identificador da conta. O que muda entre ambientes é o conteúdo do mapa, como já acontece com a URL da fila.

## Consequências

- Remetente que o mapa não nomeia, e corpo declarando provedor fora da lista, vão os dois para a DLQ sem linha financeira.
- A linha de log registra a identidade observada: um mapa com o valor errado manda **toda** mensagem legítima para a DLQ, e é esse valor no log que corrige a configuração.
- Um processo sem o mapa, ou com ele malformado, não abre a porta HTTP e não consome da fila.
- A forma do identificador que a nuvem usa nunca é exercitada localmente. O risco é baixo — comparação por igualdade — e está declarado em [06-riscos-e-limitacoes](../06-riscos-e-limitacoes.md).

## Onde está no código

- `deploy/local/queue-senders.yaml` — o mapa local.
- `internal/app/receivewager/receivewager.go` — `Senders`, `Receive`.
