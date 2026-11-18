package combell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient("key", "secret")
	c.baseURL = srv.URL
	return c
}

func TestListRecordsPages(t *testing.T) {
	var all []Record
	for i := 0; i < 150; i++ {
		all = append(all, Record{ID: strconv.Itoa(i), Type: "A", RecordName: "r", Content: "1.2.3.4", TTL: 3600})
	}

	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v2/dns/example.com/records" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "hmac key:") {
			t.Errorf("missing hmac authorization header")
		}
		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		take, _ := strconv.Atoi(r.URL.Query().Get("take"))
		end := min(skip+take, len(all))
		w.Header().Set("X-Paging-TotalResults", strconv.Itoa(len(all)))
		json.NewEncoder(w).Encode(all[skip:end])
	})

	got, err := c.ListRecords(context.Background(), "Example.COM")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if len(got) != 150 {
		t.Errorf("records = %d, want 150", len(got))
	}
}

func TestListDomains(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/domains" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `[{"domain_name":"a.be"},{"domain_name":"b.com"}]`)
	})

	got, err := c.ListDomains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].DomainName != "b.com" {
		t.Errorf("got %+v", got)
	}
}

func TestWriteRequests(t *testing.T) {
	type seen struct {
		method, path string
		body         Record
	}
	var last seen
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		last = seen{method: r.Method, path: r.URL.Path}
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			json.Unmarshal(b, &last.body)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ctx := context.Background()
	rec := Record{ID: "abc", Type: "TXT", RecordName: "www", TTL: 600, Content: "hello"}

	if err := c.CreateRecord(ctx, "example.com", rec); err != nil {
		t.Fatal(err)
	}
	if last.method != http.MethodPost || last.path != "/v2/dns/example.com/records" || last.body.ID != "" || last.body.Content != "hello" {
		t.Errorf("create: %+v", last)
	}

	if err := c.UpdateRecord(ctx, "example.com", rec); err != nil {
		t.Fatal(err)
	}
	if last.method != http.MethodPut || last.path != "/v2/dns/example.com/records/abc" || last.body.TTL != 600 {
		t.Errorf("update: %+v", last)
	}

	if err := c.DeleteRecord(ctx, "example.com", "abc"); err != nil {
		t.Fatal(err)
	}
	if last.method != http.MethodDelete || last.path != "/v2/dns/example.com/records/abc" {
		t.Errorf("delete: %+v", last)
	}

	if err := c.UpdateRecord(ctx, "example.com", Record{Type: "TXT"}); err == nil {
		t.Error("update without id should fail")
	}
}

func TestAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error_code":"validation_error","error_message":"bad content"}`)
	})

	err := c.DeleteRecord(context.Background(), "example.com", "1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want APIError", err)
	}
	if apiErr.StatusCode != 400 || apiErr.Code != "validation_error" || apiErr.Message != "bad content" {
		t.Errorf("got %+v", apiErr)
	}
}

func TestNames(t *testing.T) {
	cases := []struct {
		in, rel, fqdn string
	}{
		{"www", "www", "www.example.com"},
		{"www.example.com", "www", "www.example.com"},
		{"WWW.Example.COM.", "WWW", "WWW.example.com"},
		{"_acme-challenge.app", "_acme-challenge.app", "_acme-challenge.app.example.com"},
		{"example.com", "@", "example.com"},
		{"@", "@", "example.com"},
		{"", "", "example.com"},
		{"notexample.com", "notexample.com", "notexample.com.example.com"},
	}
	for _, c := range cases {
		if got := RelativeName(c.in, "Example.com"); got != c.rel {
			t.Errorf("RelativeName(%q) = %q, want %q", c.in, got, c.rel)
		}
		if got := FQDN(c.in, "Example.com"); got != c.fqdn {
			t.Errorf("FQDN(%q) = %q, want %q", c.in, got, c.fqdn)
		}
	}
}
