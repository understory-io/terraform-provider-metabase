// Package metabase is a minimal Metabase REST API client used by the
// terraform-provider-metabase resources. It only implements the surface the
// provider needs: collections, permission groups and memberships, users,
// databases, and the two revisioned permission graphs.
package metabase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client talks to the Metabase REST API using an API key.
//
// GET responses are memoised until the next write. A plan refreshes one
// resource per collection, membership and (group, database) pair, and each of
// them reads the same few documents (the graphs, the membership map, the user
// list); without the memo a plan of the Understory instance is several hundred
// identical requests. Any non-GET request clears the memo, so a read after a
// write always sees the write.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	userAgent  string

	mu    sync.Mutex
	cache map[string][]byte
	gen   uint64 // bumped by every write; a read only memoises if it is unchanged

	// graphMu serialises read-modify-write cycles on the permission graphs.
	// Terraform applies resources in parallel, and every graph write carries
	// the revision it was based on, so two concurrent writers would make each
	// other fail with 409. Holding this across GET + PUT turns that into a
	// queue within one run; the 409 retry covers writers outside this process.
	graphMu sync.Mutex
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the underlying http.Client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// WithUserAgent sets the User-Agent header sent with every request.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// New constructs a Client for the Metabase instance at baseURL.
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
		userAgent:  "terraform-provider-metabase",
		cache:      map[string][]byte{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError is returned for non-2xx Metabase responses.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("metabase api: %s %s -> %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// IsNotFound reports whether err is an APIError with status 404.
func IsNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }

// IsConflict reports whether err is an APIError with status 409, which is
// what a graph write based on a stale revision gets back.
func IsConflict(err error) bool { return hasStatus(err, http.StatusConflict) }

func hasStatus(err error, status int) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == status
	}
	return false
}

// Invalidate drops every memoised GET response.
func (c *Client) Invalidate() {
	c.mu.Lock()
	c.cache = map[string][]byte{}
	c.gen++
	c.mu.Unlock()
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	key := path
	if len(query) > 0 {
		key += "?" + query.Encode()
	}
	c.mu.Lock()
	body, ok := c.cache[key]
	gen := c.gen
	c.mu.Unlock()
	if !ok {
		var err error
		body, err = c.request(ctx, http.MethodGet, path, query, nil)
		if err != nil {
			return err
		}
		c.mu.Lock()
		if c.gen == gen {
			c.cache[key] = body
		}
		c.mu.Unlock()
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (c *Client) write(ctx context.Context, method, path string, query url.Values, in, out any) error {
	// Before and after: a read that started before this write and finishes
	// after it must not be memoised, and the generation bump on the second
	// call is what stops it.
	c.Invalidate()
	body, err := c.request(ctx, method, path, query, in)
	c.Invalidate()
	if err != nil {
		return err
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, in any) ([]byte, error) {
	var reqBody io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{StatusCode: resp.StatusCode, Method: method, Path: path, Body: string(respBody)}
	}
	return respBody, nil
}
