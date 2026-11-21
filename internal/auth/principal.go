package auth

import "context"

const (
	OwnerTokenPrefix = "ssu_"
	ScopeOwner       = "owner"
	ScopeViewer      = "viewer"
	RootTokenID      = "root"
)

type Principal struct {
	TokenID string
	Scope   string
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func AuditNote(ctx context.Context, note string) string {
	p, ok := PrincipalFrom(ctx)
	if !ok || p.TokenID == "" {
		return note
	}
	if note == "" {
		return "token=" + p.TokenID
	}
	return "token=" + p.TokenID + "; " + note
}
