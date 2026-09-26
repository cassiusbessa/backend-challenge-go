# 0032. O apply do broker como serviço de execução única do Compose

Status: aceita · 2026-09-26

## Contexto

`docker compose up --build` subia o processo diante de um LocalStack vazio: o apply do Terraform era um passo à parte, no host, e o README o apresentava depois das rotas. Sem o tópico, o relay recebe `NotFoundException`, que o classificador trata como recusa permanente — de propósito, porque na AWS é erro de configuração —, e na décima recusa, cerca de quatro minutos depois, marca a linha da outbox como morta; os eventos gravados antes do apply não saem nunca. O LocalStack community não persiste, então todo reinício do broker recria esse estado. `make provision` e o CI dependiam de Terraform instalado.

## Opções consideradas

1. Manter o apply no host e reordenar o README para ele vir antes da primeira rota.
2. Scripts de inicialização do LocalStack (`/etc/localstack/init/ready.d`) criando filas, tópico e remetente com `awslocal`.
3. `tflocal` no host, que troca o endpoint do provider sem mudar o `.tf`.
4. Um serviço de execução única no Compose, com a imagem oficial do Terraform, de que o processo depende como já depende do `migrate`.

## Decisão

Opção 4. A opção 1 não tira o Terraform do host, e o estado que perde eventos continua a um passo pulado de distância. A opção 2 rodaria a cada partida do broker, que é justamente o que falta, mas descreveria as filas uma segunda vez fora do Terraform: duas descrições se afastam, e o `maxReceiveCount` de 15 e a invisibilidade de 30 s já foram ajustados no `.tf` depois de medidos. A opção 3 é outra ferramenta no host para trocar só o endereço, o que uma variável do provider já faz.

O estado e a chave do remetente continuam no diretório montado, onde o README os aponta, e o último passo do serviço os devolve ao dono do diretório; o cache do provider vai para um volume. Estado em volume esconderia a chave do operador, e um `user:` interpolado do ambiente não serve, porque o `docker compose up` puro não exporta `UID` e o padrão seria root de qualquer jeito.

## Consequências

- O processo só sobe depois de o apply terminar com sucesso. Um apply que falha faz o `up` falhar nomeando o `provision`, em vez de deixar o processo de pé diante de um broker sem tópico.
- Toda subida roda o apply de novo, e `docker compose restart localstack` também, pela dependência com `restart: true`; sobre um broker provisionado ele termina sem mudança. `make provision` e o CI rodam o mesmo serviço, e a versão do Terraform fica escrita só no `compose.yaml`.
- O lock do provider traz os hashes de `linux_amd64` e `linux_arm64`, para o `init` do contêiner não reescrever um arquivo versionado num host ARM.
- **O que se paga:** o primeiro `up`, e cada um depois de um `down -v`, baixa os providers — cerca de 700 MB no volume. Um reinício do broker por fora do Compose, como `docker restart` ou o daemon voltando, não dispara nada: até alguém rodar `make provision`, o relay volta a contar recusas. E um apply que falha derruba a subida inteira, como o `migrate` já derrubava.

## Onde está no código

- `compose.yaml` — o serviço `provision`, o volume `terraform` e a dependência do `wager`.
- `deploy/terraform/localstack/variables.tf` — `localstack_endpoint`, com o padrão do host.
- `deploy/terraform/localstack/.terraform.lock.hcl` — os hashes das duas arquiteturas.
- `internal/platform/broker/classify.go` — a recusa permanente do tópico inexistente, que esta decisão não muda.
- `Makefile` — o alvo `provision`; `.github/workflows/ci.yml` — o passo `provision the local broker`.
