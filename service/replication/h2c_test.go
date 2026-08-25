package replication

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"

	"golang.org/x/net/http2"
)

// testH2CClient speaks HTTP/2 over plaintext. Sync is bidirectional and
// Connect carries bidirectional streams over HTTP/2 only, so the default
// HTTP/1.1 client cannot open one.
func testH2CClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}
