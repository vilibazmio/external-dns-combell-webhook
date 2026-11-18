package main

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"

	"github.com/vilibazmio/external-dns-combell-webhook/combell"
)

type fakeClient struct {
	domains []string
	records map[string][]combell.Record
	nextID  int
	calls   []string
}

func (f *fakeClient) ListDomains(context.Context) ([]combell.Domain, error) {
	var out []combell.Domain
	for _, d := range f.domains {
		out = append(out, combell.Domain{DomainName: d})
	}
	return out, nil
}

func (f *fakeClient) ListRecords(_ context.Context, domain string) ([]combell.Record, error) {
	return slices.Clone(f.records[domain]), nil
}

func (f *fakeClient) CreateRecord(_ context.Context, domain string, r combell.Record) error {
	f.nextID++
	r.ID = fmt.Sprintf("new%d", f.nextID)
	f.records[domain] = append(f.records[domain], r)
	f.calls = append(f.calls, fmt.Sprintf("create %s %s %s %s %d", domain, r.Type, r.RecordName, r.Content, r.TTL))
	return nil
}

func (f *fakeClient) UpdateRecord(_ context.Context, domain string, r combell.Record) error {
	for i := range f.records[domain] {
		if f.records[domain][i].ID == r.ID {
			f.records[domain][i] = r
		}
	}
	f.calls = append(f.calls, fmt.Sprintf("update %s %s %d", domain, r.ID, r.TTL))
	return nil
}

func (f *fakeClient) DeleteRecord(_ context.Context, domain, id string) error {
	f.records[domain] = slices.DeleteFunc(f.records[domain], func(r combell.Record) bool { return r.ID == id })
	f.calls = append(f.calls, fmt.Sprintf("delete %s %s", domain, id))
	return nil
}

func newFake() *fakeClient {
	return &fakeClient{
		domains: []string{"example.com", "sub.example.com", "other.be"},
		records: map[string][]combell.Record{
			"example.com": {
				{ID: "1", Type: "A", RecordName: "www", TTL: 300, Content: "1.1.1.1"},
				{ID: "2", Type: "A", RecordName: "www", TTL: 300, Content: "2.2.2.2"},
				{ID: "3", Type: "TXT", RecordName: "a-www", TTL: 3600, Content: "heritage=external-dns,external-dns/owner=test"},
				{ID: "4", Type: "MX", RecordName: "@", TTL: 3600, Content: "10 mail.example.com"},
				{ID: "5", Type: "CNAME", RecordName: "@", TTL: 3600, Content: "target.example.net"},
			},
			"sub.example.com": {
				{ID: "6", Type: "A", RecordName: "app", TTL: 60, Content: "3.3.3.3"},
			},
		},
	}
}

func TestRecords(t *testing.T) {
	p := newProvider(newFake(), []string{"example.com"})
	got, err := p.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, ep := range got {
		names = append(names, ep.String())
	}
	want := []*endpoint.Endpoint{
		endpoint.NewEndpointWithTTL("www.example.com", "A", 300, "1.1.1.1", "2.2.2.2"),
		endpoint.NewEndpointWithTTL("a-www.example.com", "TXT", 3600, "heritage=external-dns,external-dns/owner=test"),
		endpoint.NewEndpointWithTTL("example.com", "CNAME", 3600, "target.example.net"),
		endpoint.NewEndpointWithTTL("app.sub.example.com", "A", 60, "3.3.3.3"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", names)
	}
	for i := range want {
		if got[i].String() != want[i].String() {
			t.Errorf("endpoint %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestApplyChanges(t *testing.T) {
	f := newFake()
	p := newProvider(f, nil)

	changes := &plan.Changes{
		Create: []*endpoint.Endpoint{
			endpoint.NewEndpoint("new.sub.example.com", "A", "4.4.4.4"),
			endpoint.NewEndpoint("a-new.sub.example.com", "TXT", `"heritage=external-dns,external-dns/owner=test"`),
			endpoint.NewEndpoint("nozone.example.org", "A", "5.5.5.5"),
		},
		UpdateOld: []*endpoint.Endpoint{
			endpoint.NewEndpointWithTTL("www.example.com", "A", 300, "1.1.1.1", "2.2.2.2"),
		},
		UpdateNew: []*endpoint.Endpoint{
			endpoint.NewEndpointWithTTL("www.example.com", "A", 30, "2.2.2.2", "9.9.9.9"),
		},
		Delete: []*endpoint.Endpoint{
			endpoint.NewEndpoint("a-www.example.com", "TXT", `"heritage=external-dns,external-dns/owner=test"`),
			endpoint.NewEndpoint("example.com", "CNAME", "target.example.net."),
		},
	}
	if err := p.ApplyChanges(context.Background(), changes); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"delete example.com 3",
		"delete example.com 5",
		"delete example.com 1",
		"update example.com 2 60",
		"create example.com A www 9.9.9.9 60",
		"create sub.example.com A new 4.4.4.4 3600",
		"create sub.example.com TXT a-new heritage=external-dns,external-dns/owner=test 3600",
	}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls:\n%v\nwant:\n%v", f.calls, want)
	}
	if !slices.ContainsFunc(f.records["example.com"], func(r combell.Record) bool { return r.Type == "MX" }) {
		t.Error("unmanaged MX record was touched")
	}
}

func TestDomainFilterSkipsZones(t *testing.T) {
	f := newFake()
	p := newProvider(f, []string{"other.be"})
	err := p.ApplyChanges(context.Background(), &plan.Changes{
		Delete: []*endpoint.Endpoint{endpoint.NewEndpoint("www.example.com", "A", "1.1.1.1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v, want none", f.calls)
	}
}

func TestAdjustEndpoints(t *testing.T) {
	p := newProvider(newFake(), nil)
	got, _ := p.AdjustEndpoints([]*endpoint.Endpoint{
		endpoint.NewEndpointWithTTL("a.example.com", "A", 10, "1.1.1.1"),
		endpoint.NewEndpointWithTTL("b.example.com", "A", 100000, "1.1.1.1"),
		endpoint.NewEndpoint("c.example.com", "A", "1.1.1.1"),
	})
	for i, want := range []endpoint.TTL{60, 86400, 0} {
		if got[i].RecordTTL != want {
			t.Errorf("endpoint %d TTL = %d, want %d", i, got[i].RecordTTL, want)
		}
	}
}

func TestZoneFor(t *testing.T) {
	zones := []string{"example.com", "sub.example.com"}
	cases := map[string]string{
		"example.com":          "example.com",
		"www.example.com.":     "example.com",
		"a.sub.example.com":    "sub.example.com",
		"sub.example.com":      "sub.example.com",
		"notexample.com":       "",
		"www.example.org":      "",
		"WWW.Sub.Example.COM.": "sub.example.com",
	}
	for name, want := range cases {
		if got := zoneFor(zones, name); got != want {
			t.Errorf("zoneFor(%q) = %q, want %q", name, got, want)
		}
	}
}
