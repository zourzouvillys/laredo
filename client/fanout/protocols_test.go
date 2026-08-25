package fanout

import "net/http"

// testProtocols permits unencrypted HTTP/2, which a bidirectional Sync needs.
func testProtocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetHTTP2(true)
	p.SetUnencryptedHTTP2(true)
	return p
}
