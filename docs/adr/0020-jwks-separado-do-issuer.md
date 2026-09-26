# 0020. Endereço do JWKS separado do issuer, sem descoberta OIDC na subida

Status: aceita · 2026-09-26

## Contexto

A borda valida o token do Keycloak localmente: assinatura contra a chave pública do realm, emissor e validade. A chave vem do JWKS. O protocolo OIDC oferece descoberta — buscar `/.well-known/openid-configuration` no issuer e ler de lá o endereço do JWKS. No Compose, o token anuncia `localhost` como issuer, e o processo precisa buscar a chave em `keycloak`.

## Opções consideradas

1. Descoberta OIDC na subida do processo.
2. `IDP_JWKS_URL` configurado à parte de `IDP_ISSUER`; vazio, derivado do issuer. O chaveiro remoto busca na primeira verificação e cacheia com rotação.
3. Conferir também a audiência do token.

## Decisão

Opção 2, e não conferir audiência.

A descoberta na subida põe o IdP no caminho de boot: um restart com o Keycloak fora não subiria, e a readiness — que cobre só PostgreSQL e SQS — passaria a mentir. O chaveiro remoto do `go-oidc` busca sob demanda e trata cache e rotação.

O token de `client_credentials` não carrega audiência para este serviço, então `SkipClientIDCheck` fica ligado e a decisão vai para a claim `azp`, cruzada com o mapa versionado de clientes.

## Consequências

- O processo sobe sem o IdP e responde `401` até a chave chegar.
- O realm de teste não tem mapper de audience, e os clientes estão com `fullScopeAllowed` — registrado em [06-riscos-e-limitacoes](../06-riscos-e-limitacoes.md).
- Quem autoriza é o cliente do token mais o mapa, nunca o `providerId` do corpo.

## Onde está no código

- `internal/platform/authz/verifier.go` — `NewVerifier`, `Client`.
- `internal/platform/config/config.go` — `IDP_JWKS_URL`, `parseJWKSURL`.
- `internal/platform/authz/clients.go` — o mapa.
