package service

import (
	"context"
	"net/http"

	replicationv1 "github.com/zourzouvillys/laredo/gen/laredo/replication/v1"
)

// Authorizer decides whether a caller may make a request, and — for a
// subscription — what slice of the table it may see.
//
// There was no authentication anywhere in this library. Every RPC was
// reachable by anything that could open a socket, including OAM's
// ResetSource, which drops and recreates a replication slot, and
// DrainReplication, which with an empty schema and table drains every
// fan-out target in the engine. Query's Subscribe streamed an entire table,
// live, to any caller.
//
// Two properties are deliberate.
//
// First, an Authorizer returns predicates the server ANDs into the
// subscription rather than merely approving the ones the client sent. A
// client's filters are subtractive — omitting them asks for the whole table —
// so validating what was asked for cannot constrain a caller that asks for
// everything. Imposing predicates can. This is what lets one subscriber be
// pinned server-side to its own partition.
//
// Second, when an Authorizer is configured every procedure must be allowed
// explicitly; there is no implicit pass for methods it does not recognise. A
// server with no Authorizer configured allows everything, as before — the
// library stays usable without auth, but a deployment that opts in does not
// silently leave a door open.
type Authorizer interface {
	Authorize(ctx context.Context, req AuthRequest) (AuthDecision, error)
}

// AuthRequest describes what is being attempted.
//
// Schema, Table and Filters are populated only for the Sync stream, and only
// on the second call — the one made after the client's opening SyncStart has
// been read, since that is the first moment the server knows what is being
// subscribed to. The first call, made when the stream opens, carries the
// credential and the procedure alone.
type AuthRequest struct {
	// Procedure is the full RPC path, e.g.
	// "/laredo.replication.v1.LaredoReplicationService/Sync".
	Procedure string

	// Header carries the request headers, which is where a bearer token,
	// client certificate assertion or similar will be found.
	Header http.Header

	// Peer is the remote address, when the transport exposes one.
	Peer string

	Schema   string
	Table    string
	ClientID string

	// Filters are the predicates the client asked for. They are informational:
	// an Authorizer constrains a caller through Decision.Require, not by
	// approving these.
	Filters []*replicationv1.FieldPredicate
}

// AuthDecision is the outcome of a successful authorization.
type AuthDecision struct {
	// Subject is the authenticated principal. When non-empty on a Sync
	// stream, the server uses it as the client id rather than the
	// client-supplied one, which is otherwise an unauthenticated free-form
	// string that a caller could use to impersonate another subscriber in the
	// status view.
	Subject string

	// Require are predicates the server ANDs into the subscription, in
	// addition to whatever the client asked for. Empty means no additional
	// constraint.
	Require []*replicationv1.FieldPredicate
}

// AuthorizerFunc adapts a function to the Authorizer interface.
type AuthorizerFunc func(ctx context.Context, req AuthRequest) (AuthDecision, error)

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, req AuthRequest) (AuthDecision, error) {
	return f(ctx, req)
}

// decisionKey is the context key under which a stream's authorization
// decision is carried from the interceptor to the handler.
type decisionKey struct{}

// WithDecision returns a context carrying an authorization decision.
func WithDecision(ctx context.Context, d AuthDecision) context.Context {
	return context.WithValue(ctx, decisionKey{}, d)
}

// DecisionFromContext returns the authorization decision made when the request
// arrived, if any. A handler uses it to learn the authenticated subject.
func DecisionFromContext(ctx context.Context) (AuthDecision, bool) {
	d, ok := ctx.Value(decisionKey{}).(AuthDecision)
	return d, ok
}

// authorizerKey carries the configured Authorizer to handlers that need to
// re-authorize once they know the schema and table (only Sync does).
type authorizerKey struct{}

// WithAuthorizerValue returns a context carrying the server's Authorizer.
func WithAuthorizerValue(ctx context.Context, a Authorizer) context.Context {
	return context.WithValue(ctx, authorizerKey{}, a)
}

// AuthorizerFromContext returns the server's Authorizer, if one is configured.
func AuthorizerFromContext(ctx context.Context) (Authorizer, bool) {
	a, ok := ctx.Value(authorizerKey{}).(Authorizer)
	return a, ok && a != nil
}
