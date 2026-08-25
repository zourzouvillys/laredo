package replication

import "net/http"

// testProtocols permits unencrypted HTTP/2. Sync is bidirectional and Connect
// carries bidirectional streams over HTTP/2 only, so an HTTP/1.1-only
// listener cannot serve one.
func testProtocols() *http.Protocols {
	p := new(http.Protocols)
	p.SetHTTP1(true)
	p.SetHTTP2(true)
	p.SetUnencryptedHTTP2(true)
	return p
}

// testH2CClient speaks HTTP/2 over a plaintext connection.
func testH2CClient() *http.Client {
	tr := &http.Transport{}
	p := new(http.Protocols)
	p.SetUnencryptedHTTP2(true) // HTTP/1.1 off: see client/fanout.
	tr.Protocols = p
	return &http.Client{Transport: tr}
}
