// Package scenarios holds the concurrency and failure scenarios the challenge
// statement makes mandatory, each one proved over several independent instances
// of the process against the real stack. The scenarios build only under the
// integration tag.
//
// The reader of the parameters and the proxy in front of the broker carry no
// tag: neither needs the stack, so the unit suite proves both, and the scenarios
// only use them.
package scenarios
