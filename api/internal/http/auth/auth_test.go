package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseTokens(t *testing.T) {
	tokens, err := ParseTokens(" alice:secret1 , bob:secret2 ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens["secret1"] != "alice" || tokens["secret2"] != "bob" {
		t.Fatalf("wrong mapping: %v", tokens)
	}
}

func TestParseTokens_EmptyDisablesAuth(t *testing.T) {
	tokens, err := ParseTokens("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected empty map, got %v", tokens)
	}
}

func TestParseTokens_Malformed(t *testing.T) {
	for _, spec := range []string{"alice", "alice:", ":secret", "alice:s1,alice2:s1"} {
		if _, err := ParseTokens(spec); err == nil {
			t.Errorf("expected error for %q", spec)
		}
	}
}

func TestActor_DefaultsToAnonymous(t *testing.T) {
	if got := Actor(t.Context()); got != AnonymousActor {
		t.Fatalf("expected %q, got %q", AnonymousActor, got)
	}
}

func middlewareProbe(t *testing.T) (http.Handler, *string) {
	t.Helper()
	var seenActor string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenActor = Actor(r.Context())
	})
	return Middleware(map[string]string{"secret1": "alice"}, next), &seenActor
}

func TestMiddleware_RejectsMissingAndWrongToken(t *testing.T) {
	handler, _ := middlewareProbe(t)

	for name, req := range map[string]*http.Request{
		"no token":     httptest.NewRequest(http.MethodGet, "/api/packages", nil),
		"wrong bearer": authedRequest("wrong"),
		"wrong query":  httptest.NewRequest(http.MethodGet, "/api/packages?access_token=wrong", nil),
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: expected 401, got %d", name, rec.Code)
		}
	}
}

func TestMiddleware_AcceptsBearerHeader(t *testing.T) {
	handler, seenActor := middlewareProbe(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authedRequest("secret1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if *seenActor != "alice" {
		t.Fatalf("expected actor alice, got %q", *seenActor)
	}
}

func TestMiddleware_AcceptsQueryParamFallback(t *testing.T) {
	// EventSource and <a href> downloads can't set headers, so
	// ?access_token= must work too.
	handler, seenActor := middlewareProbe(t)

	req := httptest.NewRequest(http.MethodGet, "/api/cases/x/events?access_token=secret1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if *seenActor != "alice" {
		t.Fatalf("expected actor alice, got %q", *seenActor)
	}
}

func authedRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/packages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}
