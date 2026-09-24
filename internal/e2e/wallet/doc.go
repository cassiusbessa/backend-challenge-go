// Package wallet holds the journey of the wallet routes, which builds only
// under the integration tag.
//
// This untagged file exists so that `go build` and `go vet` pointed at the
// package do not fail with "build constraints exclude all Go files".
package wallet
