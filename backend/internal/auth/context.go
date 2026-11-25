package auth

import "context"

// OperatorContext represents the authenticated Grafana operator extracted from
// an internal signed JWT or gated legacy proxy headers.
type OperatorContext struct {
	Login            string
	Email            string
	Name             string
	Role             string
	Permissions      []string
	OrgID            int64
	LookupByUsername bool
}

type operatorCtxKey struct{}

// WithOperator attaches OperatorContext to ctx.
func WithOperator(ctx context.Context, op OperatorContext) context.Context {
	return context.WithValue(ctx, operatorCtxKey{}, op)
}

// OperatorFromContext extracts OperatorContext from ctx.
func OperatorFromContext(ctx context.Context) (OperatorContext, bool) {
	op, ok := ctx.Value(operatorCtxKey{}).(OperatorContext)
	return op, ok
}
