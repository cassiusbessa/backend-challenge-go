// Package scenarios holds the concurrency and failure scenarios the challenge
// statement makes mandatory, each one proved over several independent instances
// of the process against the real stack. The scenarios build only under the
// integration tag.
//
// The reader of the parameters, the proxy in front of the broker and the
// credential that renews the token of a client carry no tag: none needs the
// stack, so the unit suite proves them, and the scenarios only use them.
package scenarios
