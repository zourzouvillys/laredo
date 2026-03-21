// Package httpsync implements a SyncTarget that forwards baseline rows and
// change events as batched HTTP requests to an external service.
package httpsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/zourzouvillys/laredo"
)

// Target implements laredo.SyncTarget by forwarding changes via HTTP.
type Target struct {
	cfg    config
	client *http.Client

	mu           sync.Mutex
	baselineBuf  []laredo.Row
	changeBuf    []changeEntry
	durable      bool
	lastFlushErr error
}

type changeEntry struct {
	Action   string     `json:"action"`
	Row      laredo.Row `json:"row,omitempty"`
	Identity laredo.Row `json:"identity,omitempty"`
}

type config struct {
	baseURL    string
	batchSize  int
	timeout    time.Duration
	retryCount int
	authHeader string
	headers    map[string]string
}

// Option configures the HTTP sync target.
type Option func(*config)

// BaseURL sets the base URL for HTTP requests (required).
func BaseURL(url string) Option {
	return func(c *config) { c.baseURL = url }
}

// BatchSize sets the number of rows/changes to buffer before flushing (default 100).
func BatchSize(n int) Option {
	return func(c *config) { c.batchSize = n }
}

// Timeout sets the HTTP request timeout (default 30s).
func Timeout(d time.Duration) Option {
	return func(c *config) { c.timeout = d }
}

// RetryCount sets the number of retries on HTTP failure (default 3).
func RetryCount(n int) Option {
	return func(c *config) { c.retryCount = n }
}

// AuthHeader sets the Authorization header value.
func AuthHeader(value string) Option {
	return func(c *config) { c.authHeader = value }
}

// Headers adds custom HTTP headers to all requests.
func Headers(h map[string]string) Option {
	return func(c *config) { c.headers = h }
}

// New creates a new HTTP sync target with the given options.
func New(opts ...Option) *Target {
	cfg := config{
		batchSize:  100,
		timeout:    30 * time.Second,
		retryCount: 3,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Target{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.timeout,
		},
	}
}

var _ laredo.SyncTarget = (*Target)(nil)

// OnInit signals the remote service that a baseline is starting.
//
//nolint:revive // implements SyncTarget.
func (t *Target) OnInit(ctx context.Context, table laredo.TableIdentifier, columns []laredo.ColumnDefinition) error {
	payload := map[string]any{
		"table":   table.String(),
		"columns": columns,
	}
	return t.postJSON(ctx, t.cfg.baseURL+"/baseline/start", payload)
}

// OnBaselineRow buffers a baseline row and flushes when the batch is full.
//
//nolint:revive // implements SyncTarget.
func (t *Target) OnBaselineRow(ctx context.Context, _ laredo.TableIdentifier, row laredo.Row) error {
	t.mu.Lock()
	t.baselineBuf = append(t.baselineBuf, row)
	if len(t.baselineBuf) >= t.cfg.batchSize {
		batch := t.baselineBuf
		t.baselineBuf = nil
		t.mu.Unlock()
		return t.flushBaselineBatch(ctx, batch)
	}
	t.mu.Unlock()
	return nil
}

// OnBaselineComplete flushes remaining buffered rows and signals completion.
//
//nolint:revive // implements SyncTarget.
func (t *Target) OnBaselineComplete(ctx context.Context, _ laredo.TableIdentifier) error {
	t.mu.Lock()
	batch := t.baselineBuf
	t.baselineBuf = nil
	t.mu.Unlock()

	if len(batch) > 0 {
		if err := t.flushBaselineBatch(ctx, batch); err != nil {
			return err
		}
	}

	return t.postJSON(ctx, t.cfg.baseURL+"/baseline/complete", map[string]any{})
}

// OnInsert buffers an insert change and flushes when the batch is full.
//
//nolint:revive // implements SyncTarget.
func (t *Target) OnInsert(ctx context.Context, _ laredo.TableIdentifier, row laredo.Row) error {
	return t.bufferChange(ctx, changeEntry{Action: "INSERT", Row: row})
}

// OnUpdate buffers an update change and flushes when the batch is full.
//
//nolint:revive // implements SyncTarget.
func (t *Target) OnUpdate(ctx context.Context, _ laredo.TableIdentifier, row laredo.Row, identity laredo.Row) error {
	return t.bufferChange(ctx, changeEntry{Action: "UPDATE", Row: row, Identity: identity})
}

// OnDelete buffers a delete change and flushes when the batch is full.
//
//nolint:revive // implements SyncTarget.
func (t *Target) OnDelete(ctx context.Context, _ laredo.TableIdentifier, identity laredo.Row) error {
	return t.bufferChange(ctx, changeEntry{Action: "DELETE", Identity: identity})
}

// OnTruncate sends a truncate event immediately (not batched).
//
//nolint:revive // implements SyncTarget.
func (t *Target) OnTruncate(ctx context.Context, _ laredo.TableIdentifier) error {
	// Flush any buffered changes first.
	if err := t.flushChanges(ctx); err != nil {
		return err
	}
	return t.postJSON(ctx, t.cfg.baseURL+"/changes", []changeEntry{{Action: "TRUNCATE"}})
}

// IsDurable reports whether the last batch flush was successful.
//
//nolint:revive // implements SyncTarget.
func (t *Target) IsDurable() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.durable
}

// OnSchemaChange returns CONTINUE — the remote service handles schema changes.
//
//nolint:revive // implements SyncTarget.
func (t *Target) OnSchemaChange(_ context.Context, _ laredo.TableIdentifier, _, _ []laredo.ColumnDefinition) laredo.SchemaChangeResponse {
	return laredo.SchemaChangeResponse{Action: laredo.SchemaContinue}
}

// ExportSnapshot returns empty — HTTP sync is stateless.
//
//nolint:revive // implements SyncTarget.
func (t *Target) ExportSnapshot(_ context.Context) ([]laredo.SnapshotEntry, error) {
	return nil, nil
}

// RestoreSnapshot is a no-op — HTTP sync is stateless.
//
//nolint:revive // implements SyncTarget.
func (t *Target) RestoreSnapshot(_ context.Context, _ laredo.TableSnapshotInfo, _ []laredo.SnapshotEntry) error {
	return nil
}

//nolint:revive // implements SyncTarget.
func (t *Target) SupportsConsistentSnapshot() bool { return false }

// OnClose flushes any remaining buffered changes on a best-effort basis.
//
//nolint:revive // implements SyncTarget.
func (t *Target) OnClose(ctx context.Context, _ laredo.TableIdentifier) error {
	_ = t.flushChanges(ctx)
	return nil
}

// --- internal ---

func (t *Target) bufferChange(ctx context.Context, entry changeEntry) error {
	t.mu.Lock()
	t.changeBuf = append(t.changeBuf, entry)
	t.durable = false
	if len(t.changeBuf) >= t.cfg.batchSize {
		batch := t.changeBuf
		t.changeBuf = nil
		t.mu.Unlock()
		return t.flushChangeBatch(ctx, batch)
	}
	t.mu.Unlock()
	return nil
}

func (t *Target) flushChanges(ctx context.Context) error {
	t.mu.Lock()
	batch := t.changeBuf
	t.changeBuf = nil
	t.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}
	return t.flushChangeBatch(ctx, batch)
}

func (t *Target) flushBaselineBatch(ctx context.Context, rows []laredo.Row) error {
	err := t.postJSONWithRetry(ctx, t.cfg.baseURL+"/baseline/batch", map[string]any{
		"rows": rows,
	})
	t.mu.Lock()
	t.durable = err == nil
	t.lastFlushErr = err
	t.mu.Unlock()
	return err
}

func (t *Target) flushChangeBatch(ctx context.Context, changes []changeEntry) error {
	err := t.postJSONWithRetry(ctx, t.cfg.baseURL+"/changes", changes)
	t.mu.Lock()
	t.durable = err == nil
	t.lastFlushErr = err
	t.mu.Unlock()
	return err
}

func (t *Target) postJSON(ctx context.Context, url string, payload any) error {
	return t.postJSONWithRetry(ctx, url, payload)
}

func (t *Target) postJSONWithRetry(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	var lastErr error
	backoff := 100 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for attempt := range t.cfg.retryCount + 1 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		if t.cfg.authHeader != "" {
			req.Header.Set("Authorization", t.cfg.authHeader)
		}
		for k, v := range t.cfg.headers {
			req.Header.Set(k, v)
		}

		resp, err := t.client.Do(req) //nolint:gosec // URL is from user-configured baseURL, not external input
		if err != nil {
			lastErr = fmt.Errorf("POST %s: %w", url, err)
			continue
		}
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("POST %s: status %d", url, resp.StatusCode)
	}

	return lastErr
}
