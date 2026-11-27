package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// headerList implements flag.Value for repeatable --header "Name: Value" flags.
type headerList []string

func (h *headerList) String() string { return strings.Join(*h, "; ") }

func (h *headerList) Set(v string) error {
	if _, _, ok := splitHeader(v); !ok {
		return fmt.Errorf("header must be 'Name: Value', got %q", v)
	}
	*h = append(*h, v)
	return nil
}

// clientFlags holds the shared connection flags used by every remote CLI
// command (report, funnel, chest, tail, mcp). Defaults come from LOOT_URL,
// LOOT_API_TOKEN and LOOT_API_HEADERS so secrets do not have to sit in argv.
type clientFlags struct {
	rawURL  *string
	token   *string
	headers headerList
}

func bindClientFlags(fs *flag.FlagSet) *clientFlags {
	defaultURL := strings.TrimSpace(os.Getenv("LOOT_URL"))
	if defaultURL == "" {
		defaultURL = "http://localhost:8080"
	}
	cf := &clientFlags{
		rawURL: fs.String("url", defaultURL, "Loot server base URL (env: LOOT_URL)"),
		token:  fs.String("token", os.Getenv("LOOT_API_TOKEN"), "API bearer token (env: LOOT_API_TOKEN)"),
	}
	fs.Var(&cf.headers, "header", "extra HTTP header as 'Name: Value', repeatable (env: LOOT_API_HEADERS)")
	return cf
}

func (cf *clientFlags) client() (*lootClient, error) {
	base, err := baseURL(*cf.rawURL)
	if err != nil {
		return nil, err
	}
	hdrs := http.Header{}
	if envH := os.Getenv("LOOT_API_HEADERS"); strings.TrimSpace(envH) != "" {
		for _, entry := range splitEnvHeaders(envH) {
			k, v, ok := splitHeader(entry)
			if !ok {
				return nil, fmt.Errorf("bad LOOT_API_HEADERS entry %q (expected 'Name: Value')", entry)
			}
			hdrs.Add(k, v)
		}
	}
	for _, entry := range cf.headers {
		k, v, ok := splitHeader(entry)
		if !ok {
			return nil, fmt.Errorf("bad --header %q (expected 'Name: Value')", entry)
		}
		hdrs.Add(k, v)
	}
	return &lootClient{
		BaseURL: base,
		Token:   strings.TrimSpace(*cf.token),
		Headers: hdrs,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// lootClient talks to a running Loot server over HTTP or WebSocket, applying
// token authentication and any extra proxy headers (e.g. Cloudflare Access).
type lootClient struct {
	BaseURL string
	Token   string
	Headers http.Header
	HTTP    *http.Client
}

// DoJSON sends one JSON HTTP request and returns the raw JSON body. It
// implements internal/mcp.Doer.
func (c *lootClient) DoJSON(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyBytes = b
	}
	endpoint := c.BaseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var raw json.RawMessage
	if err := c.requestJSON(ctx, method, endpoint, bodyBytes, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *lootClient) requestJSON(ctx context.Context, method, endpoint string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.applyAuthHeaders(req.Header, endpoint)

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read %s: %w", endpoint, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, endpoint, resp.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// applyAuthHeaders copies extra headers and attaches the API token. When the
// target URL carries Basic Auth userinfo or the caller already supplied an
// Authorization header, the token is sent as X-API-Key so it does not clobber
// reverse-proxy credentials.
func (c *lootClient) applyAuthHeaders(dst http.Header, rawURL string) {
	for k, vals := range c.Headers {
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
	if c.Token == "" {
		return
	}
	hasBasicUser := false
	if u, err := url.Parse(rawURL); err == nil && u.User != nil {
		hasBasicUser = true
	}
	if hasBasicUser || dst.Get("Authorization") != "" {
		if dst.Get("X-API-Key") == "" {
			dst.Set("X-API-Key", c.Token)
		}
		return
	}
	dst.Set("Authorization", "Bearer "+c.Token)
}

// WebSocketHeader builds the HTTP headers for dialing /ws.
func (c *lootClient) WebSocketHeader(wsURL string) http.Header {
	h := http.Header{}
	c.applyAuthHeaders(h, wsURL)
	return h
}

func splitHeader(s string) (name, value string, ok bool) {
	k, v, found := strings.Cut(s, ":")
	if !found {
		return "", "", false
	}
	k = strings.TrimSpace(k)
	v = strings.TrimSpace(v)
	if k == "" {
		return "", "", false
	}
	return k, v, true
}

func splitEnvHeaders(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == ';' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
