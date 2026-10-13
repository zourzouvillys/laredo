package service

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// authInterceptor runs the configured Authorizer for every inbound request and
// makes both the decision and the Authorizer itself available to the handler.
//
// Handlers that know their target up front are fully authorized here. Sync is
// the exception: what it subscribes to arrives in the client's first message,
// so it authorizes again once it has read that, which is where imposed
// predicates and the subject-derived client id are applied.
func authInterceptor(a Authorizer) connect.Interceptor {
	return &authInterceptorImpl{auth: a}
}

type authInterceptorImpl struct {
	auth Authorizer
}

// WrapUnary authorizes a unary request before the handler runs.
func (i *authInterceptorImpl) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		d, err := i.auth.Authorize(ctx, AuthRequest{
			Procedure: req.Spec().Procedure,
			Header:    req.Header(),
			Peer:      req.Peer().Addr,
		})
		if err != nil {
			return nil, asPermissionDenied(err)
		}
		return next(WithAuthorizerValue(WithDecision(ctx, d), i.auth), req)
	}
}

// WrapStreamingClient is a no-op; this interceptor is server-side only.
func (i *authInterceptorImpl) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	// Client-side interception is not this interceptor's job.
	return next
}

// WrapStreamingHandler authorizes a stream when it opens.
func (i *authInterceptorImpl) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		d, err := i.auth.Authorize(ctx, AuthRequest{
			Procedure: conn.Spec().Procedure,
			Header:    conn.RequestHeader(),
			Peer:      conn.Peer().Addr,
		})
		if err != nil {
			return asPermissionDenied(err)
		}
		return next(WithAuthorizerValue(WithDecision(ctx, d), i.auth), conn)
	}
}

// asPermissionDenied normalises an Authorizer's refusal. An Authorizer may
// return a connect error of its own to choose the code — Unauthenticated for a
// missing credential, say — and anything else becomes PermissionDenied rather
// than leaking as an internal error.
func asPermissionDenied(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodePermissionDenied, err)
}
