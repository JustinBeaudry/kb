// Package service owns orchestration and consumer-defined persistence ports.
package service

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type Tx = domain.Tx

// Transactor executes fn once inside a single atomic transaction. Callbacks must
// keep network effects outside the transaction and must not retain Tx.
type Transactor interface {
	Within(ctx context.Context, fn func(context.Context, Tx) error) error
}
