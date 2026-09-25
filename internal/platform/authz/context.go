package authz

import "context"

// clientKey is the key of the resolved client in the request context. The type is
// unexported, so no other package can put a client there: what the handler reads
// is what the guard resolved from the token.
type clientKey struct{}

func withClient(ctx context.Context, client Client) context.Context {
	return context.WithValue(ctx, clientKey{}, client)
}

// ClientOf answers the client the guard resolved and reports whether there is one.
// A handler outside the guard has none, and it gets the zero Client rather than a
// panic.
func ClientOf(ctx context.Context) (Client, bool) {
	client, ok := ctx.Value(clientKey{}).(Client)
	return client, ok
}
