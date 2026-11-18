package combell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.combell.com"
	apiVersion     = "/v2"
	pageSize       = 100
	maxRetries     = 3
	// Stops the paging loop if the server ever ignores skip.
	maxItems = 10000
)

type Domain struct {
	DomainName string `json:"domain_name"`
}

type Record struct {
	ID         string `json:"id,omitempty"`
	Type       string `json:"type"`
	RecordName string `json:"record_name"`
	TTL        int    `json:"ttl,omitempty"`
	Content    string `json:"content"`
}

type Client struct {
	apiKey     string
	apiSecret  string
	baseURL    string
	httpClient *http.Client
}

func NewClient(apiKey, apiSecret string) *Client {
	return &Client{
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError carries the error code and message the API returns on failure.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("combell api: %d %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("combell api: %d: %s", e.StatusCode, e.Message)
}

func (c *Client) ListDomains(ctx context.Context) ([]Domain, error) {
	return listAll[Domain](ctx, c, apiVersion+"/domains")
}

// ListRecords returns every record in the zone. No filter query parameters are
// used: the path is part of the signed string, and Combell's documentation and
// SDK disagree on whether it is lowercased first. A fully lowercase path signs
// the same either way.
func (c *Client) ListRecords(ctx context.Context, domain string) ([]Record, error) {
	return listAll[Record](ctx, c, fmt.Sprintf("%s/dns/%s/records", apiVersion, normalize(domain)))
}

func (c *Client) CreateRecord(ctx context.Context, domain string, r Record) error {
	r.ID = ""
	payload, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encoding record: %w", err)
	}
	_, _, err = c.do(ctx, http.MethodPost, fmt.Sprintf("%s/dns/%s/records", apiVersion, normalize(domain)), payload)
	return err
}

func (c *Client) UpdateRecord(ctx context.Context, domain string, r Record) error {
	if r.ID == "" {
		return fmt.Errorf("record has no id")
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encoding record: %w", err)
	}
	_, _, err = c.do(ctx, http.MethodPut, fmt.Sprintf("%s/dns/%s/records/%s", apiVersion, normalize(domain), r.ID), payload)
	return err
}

func (c *Client) DeleteRecord(ctx context.Context, domain, recordID string) error {
	_, _, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/dns/%s/records/%s", apiVersion, normalize(domain), recordID), nil)
	return err
}

func normalize(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}

func listAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var out []T
	for skip := 0; skip < maxItems; skip += pageSize {
		body, headers, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s?skip=%d&take=%d", path, skip, pageSize), nil)
		if err != nil {
			return nil, err
		}

		var page []T
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decoding response of %s: %w", path, err)
		}
		out = append(out, page...)

		if len(page) < pageSize {
			return out, nil
		}
		total, err := strconv.Atoi(headers.Get("X-Paging-TotalResults"))
		if err == nil && skip+pageSize >= total {
			return out, nil
		}
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, method, pathAndQuery string, body []byte) ([]byte, http.Header, error) {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		nonce, err := newNonce()
		if err != nil {
			return nil, nil, err
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathAndQuery, bytes.NewReader(body))
		if err != nil {
			return nil, nil, fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("Authorization", authorization(c.apiKey, c.apiSecret, method, pathAndQuery, body, nonce, time.Now().Unix()))
		req.Header.Set("Accept", "application/json")
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("calling combell: %w", err)
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, nil, fmt.Errorf("reading response: %w", readErr)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = parseAPIError(resp.StatusCode, respBody)
			wait := retryAfter(resp.Header)
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, nil, parseAPIError(resp.StatusCode, respBody)
		}

		return respBody, resp.Header, nil
	}

	return nil, nil, fmt.Errorf("giving up after %d rate limited attempts: %w", maxRetries, lastErr)
}

func retryAfter(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 5 * time.Second
}

func parseAPIError(status int, body []byte) error {
	var parsed struct {
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && (parsed.ErrorCode != "" || parsed.ErrorMessage != "") {
		return &APIError{StatusCode: status, Code: parsed.ErrorCode, Message: parsed.ErrorMessage}
	}
	return &APIError{StatusCode: status, Message: strings.TrimSpace(string(body))}
}
