package domain

import (
	"context"
	"strings"
)

type requestIDContextKey struct{}

// WithRequestID carries an application-generated crypto/rand.Text ID. It must
// not accept arbitrary header values or source strings as telemetry identity.
func WithRequestID(ctx context.Context, id string) context.Context {
	if len(id) != 26 {
		return ctx
	}
	for _, r := range id {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", r) {
			return ctx
		}
	}
	return context.WithValue(ctx, requestIDContextKey{}, id)
}
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}
