package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// MockHTTPServer is a configurable mock HTTP server for testing HTTP sync targets.
// It records all requests for later assertion.
type MockHTTPServer struct {
	Server *httptest.Server
	URL    string

	mu       sync.Mutex
	Requests []RecordedRequest
}

// RecordedRequest is a captured HTTP request.
type RecordedRequest struct {
	Method string
	Path   string
	Body   json.RawMessage
}

// NewMockHTTPServer creates a mock HTTP server that records all requests
// and responds with 200 OK. The server is automatically closed when the
// test finishes.
func NewMockHTTPServer(t *testing.T) *MockHTTPServer {
	t.Helper()

	m := &MockHTTPServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.Requests = append(m.Requests, RecordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   body,
		})
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	m.URL = m.Server.URL

	t.Cleanup(func() { m.Server.Close() })
	return m
}

// RequestsByPath returns all recorded requests matching the given path.
func (m *MockHTTPServer) RequestsByPath(path string) []RecordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []RecordedRequest
	for _, r := range m.Requests {
		if r.Path == path {
			result = append(result, r)
		}
	}
	return result
}

// RequestCount returns the total number of recorded requests.
func (m *MockHTTPServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Requests)
}
