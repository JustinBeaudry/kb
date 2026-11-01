package domain

import (
	"context"
	"crypto/rand"
	"testing"
)

func TestRequestIDRoundTripsOnlyGeneratedFormat(t *testing.T) {
	ctx := context.Background()
	if RequestID(ctx) != "" {
		t.Fatal("empty request context has an ID")
	}
	id := rand.Text()
	if RequestID(WithRequestID(ctx, id)) != id {
		t.Fatal("generated request ID unavailable")
	}
	for _, invalid := range []string{"private source prose", "", "http://private.invalid", id + id} {
		if RequestID(WithRequestID(ctx, invalid)) != "" {
			t.Fatal("unsafe ID entered context")
		}
	}
}
