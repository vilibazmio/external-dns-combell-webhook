package combell

import "testing"

func TestURLEncode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/v2/dns/example.com/records?skip=0&take=100", "%2Fv2%2Fdns%2Fexample.com%2Frecords%3Fskip%3D0%26take%3D100"},
		{"/v2/dns/example.com/records", "%2Fv2%2Fdns%2Fexample.com%2Frecords"},
		{"a~b", "a%7Eb"},
		{"a b", "a+b"},
		{"-_.", "-_."},
	}

	for _, c := range cases {
		if got := urlEncode(c.in); got != c.want {
			t.Errorf("urlEncode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The expected signatures were produced from Combell's documented scheme as
// implemented by their own PHP SDK, so a change here means the signing rules
// changed, not that the test is stale.
func TestAuthorization(t *testing.T) {
	const (
		apiKey    = "testkey"
		apiSecret = "testsecret"
		nonce     = "abc123"
		timestamp = int64(1700000000)
	)

	t.Run("empty body", func(t *testing.T) {
		got := authorization(apiKey, apiSecret, "GET", "/v2/dns/example.com/records?skip=0&take=100", nil, nonce, timestamp)
		want := "hmac testkey:l0XJsrt0aDBjZyOYKtC/Dxo8LANY4Dk88apZw1yBGWU=:abc123:1700000000"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("with body", func(t *testing.T) {
		body := []byte(`{"type":"TXT","record_name":"_acme-challenge","ttl":60,"content":"value"}`)
		got := authorization(apiKey, apiSecret, "POST", "/v2/dns/example.com/records", body, nonce, timestamp)
		want := "hmac testkey:2DrxAAjk0p/Y71MWlm/mqRokLNFyt6OPTkbqPrdcVx0=:abc123:1700000000"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestNewNonceIsUnique(t *testing.T) {
	first, err := newNonce()
	if err != nil {
		t.Fatalf("newNonce: %v", err)
	}
	second, err := newNonce()
	if err != nil {
		t.Fatalf("newNonce: %v", err)
	}
	if first == second {
		t.Error("two nonces were identical")
	}
	if len(first) != 24 {
		t.Errorf("nonce length = %d, want 24", len(first))
	}
}
