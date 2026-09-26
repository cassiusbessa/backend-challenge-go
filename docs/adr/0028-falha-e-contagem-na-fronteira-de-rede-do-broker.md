# 0028. Falha e contagem na fronteira de rede do broker

Status: aceita · 2026-09-26

## Contexto

Dois cenários obrigatórios precisam de algo que o broker de verdade não faz sob comando. A interrupção precisa de uma instância que commita o desfecho de uma mensagem e morre antes de a remoção chegar à fila. A disputa entre publicadores precisa contar cada evento que sai dos processos, e o tópico FIFO deduplica pelo `eventId` numa janela de cinco minutos, então um envio duplicado chega ao assinante uma vez só. O consumidor recebe `*broker.Ingress` concreto do grafo e o relay recebe `*broker.Topic` concreto, e os dois falam HTTP com o endpoint da configuração.

## Opções consideradas

1. Uma costura no grafo de produção: prover `wagerqueue.Queue` como interface e decorá-la no teste com um `Delete` que falha, e o mesmo para o tópico.
2. Contar as publicações no assinante do tópico.
3. Parar a instância no instante certo entre o commit e a remoção.
4. Um proxy do teste entre a instância e o LocalStack, para onde a instância aponta `SQS_ENDPOINT` ou `SNS_ENDPOINT`, que recusa a remoção com um erro de cliente e anota o `MessageDeduplicationId` de cada publicação antes de repassá-la.

## Decisão

Opção 4. A opção 1 mudaria a composição de produção para servir a um teste, e a falha provocada ficaria acima do SDK, onde nenhuma falha real acontece: a política de repetição do SDK e a classificação do erro que ele devolve nunca seriam exercidas. A opção 2 esconde exatamente o defeito que o caso procura, porque a deduplicação do broker entrega uma vez o que saiu duas. A opção 3 depende de tempo: o intervalo entre o commit e a remoção é de milissegundos, e o caso seria intermitente.

Na fronteira de rede a instância não distingue o proxy do broker. A remoção recusada é um `400` que o SDK lê como recusa do broker e não repete, e o consumidor faz o que faria numa rede real: registra a falha e deixa a mensagem na fila. O caso para a instância depois que o proxy contou a recusa, o que dá a morte entre o commit e o delete sem corrida contra relógio.

## Consequências

- A interrupção e a contagem acontecem na borda do processo, sem uma linha nova no código de produção.
- O proxy conhece o protocolo de fio do SDK: JSON com a operação no cabeçalho `X-Amz-Target` para a fila, formulário com `Action` para o tópico. O teste de unidade dele usa os clientes reais do SDK contra um servidor no lugar do broker, e os cenários afirmam o que o proxy recusou e registrou — um proxy que deixasse de reconhecer a operação faz o caso falhar, em vez de passar sem ter interrompido nada.
- **O que se paga:** uma atualização do SDK que troque o protocolo de uma das duas APIs exige atualizar o proxy. E ele só conta o que passa por ele: a frota também publica eventos de outras carteiras do banco da suíte, que o caso ignora.

## Onde está no código

- `internal/e2e/scenarios/proxy.go` — `Proxy`, `Faculties` e `NewProxy`: a recusa da remoção e o registro da publicação.
- `internal/e2e/scenarios/proxy_test.go` — o reconhecimento das duas operações com os clientes reais do SDK.
- `internal/e2e/scenarios/harness_test.go` — `front`, que põe o proxy entre a instância e o broker pelo tempo do caso.
- `internal/e2e/scenarios/interruption_test.go`, `publishers_test.go` e `restart_test.go` — os três cenários que passam pelo proxy.
