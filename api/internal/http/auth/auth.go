// Package auth provides bearer-token authentication for the dictum API
// (plan.md Phase 5 hardening). Tokens are static, named credentials
// configured via DICTUM_API_TOKENS ("actor:token,actor:token") — there is
// no user directory; the private-service posture (plan.md §5) means the
// operator hands each staff member their own token, and the actor name
// attached to it is what lands in audit_log.actor.
package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// AnonymousActor is the actor recorded when auth is disabled (no tokens
// configured) — audit_log.actor is NOT NULL, so there is always a name.
const AnonymousActor = "anonymous"

type actorKey struct{}

// WithActor returns ctx carrying the authenticated actor's name.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// Actor returns the authenticated actor from ctx, or AnonymousActor if
// no auth middleware ran (auth disabled, or tests hitting handlers direct).
func Actor(ctx context.Context) string {
	if actor, ok := ctx.Value(actorKey{}).(string); ok && actor != "" {
		return actor
	}
	return AnonymousActor
}

// ParseTokens parses the DICTUM_API_TOKENS format — comma-separated
// "actor:token" pairs — into a token→actor map. An empty spec yields an
// empty map (auth disabled), not an error.
func ParseTokens(spec string) (map[string]string, error) {
	tokens := map[string]string{}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return tokens, nil
	}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		actor, token, ok := strings.Cut(pair, ":")
		actor, token = strings.TrimSpace(actor), strings.TrimSpace(token)
		if !ok || actor == "" || token == "" {
			return nil, fmt.Errorf("malformed token pair %q: want actor:token", pair)
		}
		if _, dup := tokens[token]; dup {
			return nil, fmt.Errorf("duplicate token for actor %q", actor)
		}
		tokens[token] = actor
	}
	return tokens, nil
}

// Middleware rejects requests that don't carry a configured token, and
// stamps the matching actor into the request context otherwise. The token
// is read from "Authorization: Bearer <token>", falling back to an
// ?access_token= query parameter for clients that can't set headers —
// EventSource (the SSE /events route) and plain <a href> downloads (the
// package archive link) both need the fallback.
func Middleware(tokens map[string]string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := requestToken(r)
		actor := ""
		if token != "" {
			for t, a := range tokens {
				if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
					actor = a
				}
			}
		}
		if actor == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dictum"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
	})
}

func requestToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if scheme, token, ok := strings.Cut(header, " "); ok && strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(token)
	}
	return r.URL.Query().Get("access_token")
}
