// O pacote só existe atrás da tag integration. Sem um arquivo sem tag,
// `go build` e `go vet` apontados para ele falham com "build constraints
// exclude all Go files".
package process
