package combell

import "strings"

// RelativeName turns what a user typed into the record name Combell expects,
// which is relative to the domain. "www.example.com" and "www" both become
// "www", and "example.com" becomes "@".
func RelativeName(name, domain string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	domain = normalize(domain)
	lower := strings.ToLower(name)
	switch {
	case lower == domain:
		return "@"
	case strings.HasSuffix(lower, "."+domain):
		return name[:len(name)-len(domain)-1]
	}
	return name
}

// FQDN returns the full name a record name stands for within the domain.
func FQDN(name, domain string) string {
	name = RelativeName(name, domain)
	domain = normalize(domain)
	if name == "" || name == "@" {
		return domain
	}
	return name + "." + domain
}
