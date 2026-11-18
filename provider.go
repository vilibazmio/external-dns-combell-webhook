package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"

	"github.com/vilibazmio/external-dns-combell-webhook/combell"
)

const (
	minTTL     = 60
	maxTTL     = 86400
	defaultTTL = 3600
)

// managedTypes are the record types external-dns manages by default, plus TXT
// for its ownership records. Other records in a zone are never touched.
var managedTypes = []string{
	endpoint.RecordTypeA,
	endpoint.RecordTypeAAAA,
	endpoint.RecordTypeCNAME,
	endpoint.RecordTypeTXT,
}

type combellAPI interface {
	ListDomains(ctx context.Context) ([]combell.Domain, error)
	ListRecords(ctx context.Context, domain string) ([]combell.Record, error)
	CreateRecord(ctx context.Context, domain string, r combell.Record) error
	UpdateRecord(ctx context.Context, domain string, r combell.Record) error
	DeleteRecord(ctx context.Context, domain, recordID string) error
}

type combellProvider struct {
	client       combellAPI
	domainFilter *endpoint.DomainFilter
}

var _ provider.Provider = (*combellProvider)(nil)

func newProvider(client combellAPI, domainFilter []string) *combellProvider {
	return &combellProvider{client: client, domainFilter: endpoint.NewDomainFilter(domainFilter)}
}

func (p *combellProvider) GetDomainFilter() endpoint.DomainFilterInterface {
	return p.domainFilter
}

func (p *combellProvider) AdjustEndpoints(endpoints []*endpoint.Endpoint) ([]*endpoint.Endpoint, error) {
	for _, ep := range endpoints {
		if ep.RecordTTL.IsConfigured() {
			ep.RecordTTL = endpoint.TTL(clampTTL(int(ep.RecordTTL)))
		}
	}
	return endpoints, nil
}

func (p *combellProvider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	zones, err := p.zones(ctx)
	if err != nil {
		return nil, err
	}

	var out []*endpoint.Endpoint
	for _, zone := range zones {
		records, err := p.client.ListRecords(ctx, zone)
		if err != nil {
			return nil, fmt.Errorf("listing records of %s: %w", zone, err)
		}

		byKey := map[string]*endpoint.Endpoint{}
		for _, r := range records {
			typ := strings.ToUpper(r.Type)
			if !slices.Contains(managedTypes, typ) {
				continue
			}
			name := strings.ToLower(combell.FQDN(r.RecordName, zone))
			key := name + " " + typ
			if ep, ok := byKey[key]; ok {
				ep.Targets = append(ep.Targets, r.Content)
				continue
			}
			ep := endpoint.NewEndpointWithTTL(name, typ, endpoint.TTL(r.TTL), r.Content)
			if ep == nil {
				continue
			}
			byKey[key] = ep
			out = append(out, ep)
		}
	}
	return out, nil
}

func (p *combellProvider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	zones, err := p.zones(ctx)
	if err != nil {
		return err
	}
	a := &applier{client: p.client, zones: zones, records: map[string][]combell.Record{}}

	for _, ep := range changes.Delete {
		if err := a.delete(ctx, ep, ep.Targets); err != nil {
			return err
		}
	}
	for i := range min(len(changes.UpdateOld), len(changes.UpdateNew)) {
		if err := a.update(ctx, changes.UpdateOld[i], changes.UpdateNew[i]); err != nil {
			return err
		}
	}
	for _, ep := range changes.Create {
		if err := a.create(ctx, ep, ep.Targets); err != nil {
			return err
		}
	}
	return nil
}

// zones returns the domains in the Combell account that match the domain filter.
func (p *combellProvider) zones(ctx context.Context) ([]string, error) {
	domains, err := p.client.ListDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing domains: %w", err)
	}
	var out []string
	for _, d := range domains {
		name := strings.ToLower(strings.TrimSuffix(d.DomainName, "."))
		if p.domainFilter.Match(name) {
			out = append(out, name)
		}
	}
	return out, nil
}

// applier holds the zones and the records already fetched during one
// ApplyChanges call, so each zone is listed at most once.
type applier struct {
	client  combellAPI
	zones   []string
	records map[string][]combell.Record
}

func (a *applier) create(ctx context.Context, ep *endpoint.Endpoint, targets []string) error {
	zone := zoneFor(a.zones, ep.DNSName)
	if zone == "" {
		slog.Warn("no Combell zone for record, skipping", "name", ep.DNSName)
		return nil
	}
	ttl := defaultTTL
	if ep.RecordTTL.IsConfigured() {
		ttl = clampTTL(int(ep.RecordTTL))
	}
	for _, t := range targets {
		r := combell.Record{
			Type:       ep.RecordType,
			RecordName: combell.RelativeName(ep.DNSName, zone),
			TTL:        ttl,
			Content:    content(ep.RecordType, t),
		}
		if err := a.client.CreateRecord(ctx, zone, r); err != nil {
			return fmt.Errorf("creating %s %s: %w", ep.RecordType, ep.DNSName, err)
		}
		slog.Info("created record", "name", ep.DNSName, "type", ep.RecordType, "content", r.Content)
	}
	return nil
}

func (a *applier) delete(ctx context.Context, ep *endpoint.Endpoint, targets []string) error {
	zone := zoneFor(a.zones, ep.DNSName)
	if zone == "" {
		return nil
	}
	for _, t := range targets {
		matches, err := a.find(ctx, zone, ep, t)
		if err != nil {
			return err
		}
		for _, r := range matches {
			if err := a.client.DeleteRecord(ctx, zone, r.ID); err != nil {
				return fmt.Errorf("deleting %s %s: %w", ep.RecordType, ep.DNSName, err)
			}
			a.forget(zone, r.ID)
			slog.Info("deleted record", "name", ep.DNSName, "type", ep.RecordType, "content", r.Content)
		}
	}
	return nil
}

// update removes the targets that are gone, adds the new ones and changes the
// TTL of the ones that stay when it differs.
func (a *applier) update(ctx context.Context, old, desired *endpoint.Endpoint) error {
	var removed, added, kept []string
	for _, t := range old.Targets {
		if !hasTarget(desired, t) {
			removed = append(removed, t)
		}
	}
	for _, t := range desired.Targets {
		if hasTarget(old, t) {
			kept = append(kept, t)
		} else {
			added = append(added, t)
		}
	}

	if err := a.delete(ctx, old, removed); err != nil {
		return err
	}

	zone := zoneFor(a.zones, desired.DNSName)
	if zone != "" && desired.RecordTTL.IsConfigured() {
		ttl := clampTTL(int(desired.RecordTTL))
		for _, t := range kept {
			matches, err := a.find(ctx, zone, desired, t)
			if err != nil {
				return err
			}
			for _, r := range matches {
				if r.TTL == ttl {
					continue
				}
				r.TTL = ttl
				if err := a.client.UpdateRecord(ctx, zone, r); err != nil {
					return fmt.Errorf("updating %s %s: %w", desired.RecordType, desired.DNSName, err)
				}
				slog.Info("updated record TTL", "name", desired.DNSName, "type", desired.RecordType, "ttl", ttl)
			}
		}
	}

	return a.create(ctx, desired, added)
}

// find returns the records in the zone with the endpoint's name and type and
// the given target as content.
func (a *applier) find(ctx context.Context, zone string, ep *endpoint.Endpoint, target string) ([]combell.Record, error) {
	records, ok := a.records[zone]
	if !ok {
		var err error
		records, err = a.client.ListRecords(ctx, zone)
		if err != nil {
			return nil, fmt.Errorf("listing records of %s: %w", zone, err)
		}
		a.records[zone] = records
	}

	name := strings.ToLower(strings.TrimSuffix(ep.DNSName, "."))
	want := content(ep.RecordType, target)
	var out []combell.Record
	for _, r := range records {
		if strings.EqualFold(r.Type, ep.RecordType) &&
			strings.ToLower(combell.FQDN(r.RecordName, zone)) == name &&
			sameContent(ep.RecordType, r.Content, want) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (a *applier) forget(zone, id string) {
	a.records[zone] = slices.DeleteFunc(a.records[zone], func(r combell.Record) bool { return r.ID == id })
}

func hasTarget(ep *endpoint.Endpoint, target string) bool {
	return slices.ContainsFunc(ep.Targets, func(t string) bool {
		return sameContent(ep.RecordType, content(ep.RecordType, t), content(ep.RecordType, target))
	})
}

// content is the value sent to Combell for a target. external-dns wraps its
// TXT ownership values in double quotes. Those are removed so Combell stores
// the plain text, which is also what it returns when listing.
func content(recordType, target string) string {
	if strings.EqualFold(recordType, endpoint.RecordTypeTXT) {
		if len(target) >= 2 && strings.HasPrefix(target, `"`) && strings.HasSuffix(target, `"`) {
			return target[1 : len(target)-1]
		}
		return target
	}
	return strings.TrimSuffix(target, ".")
}

func sameContent(recordType, a, b string) bool {
	if strings.EqualFold(recordType, endpoint.RecordTypeTXT) {
		return content(recordType, a) == content(recordType, b)
	}
	return strings.EqualFold(content(recordType, a), content(recordType, b))
}

// zoneFor returns the most specific zone that contains name, or "" if none does.
func zoneFor(zones []string, name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	best := ""
	for _, z := range zones {
		if (name == z || strings.HasSuffix(name, "."+z)) && len(z) > len(best) {
			best = z
		}
	}
	return best
}

func clampTTL(ttl int) int {
	return max(minTTL, min(maxTTL, ttl))
}
