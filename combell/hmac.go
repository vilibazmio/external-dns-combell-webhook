package combell

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// authorization builds the value for the Authorization header.
//
// The signed string is the api key, the lowercased method, the urlencoded path
// including the querystring, the unix timestamp, the nonce, and for a non empty
// body the base64 encoded MD5 of that body. An empty body adds nothing.
//
// This mirrors Combell's own PHP SDK (src/Combell/Handler/HmacHandler.php). Two
// details there are easy to get wrong: the secret is used as raw bytes and is
// never base64 decoded, and the path is encoded with PHP's urlencode rules
// rather than Go's url.QueryEscape.
func authorization(apiKey, apiSecret, method, pathAndQuery string, body []byte, nonce string, timestamp int64) string {
	content := ""
	if len(body) > 0 {
		sum := md5.Sum(body)
		content = base64.StdEncoding.EncodeToString(sum[:])
	}

	var sb strings.Builder
	sb.WriteString(apiKey)
	sb.WriteString(strings.ToLower(method))
	sb.WriteString(urlEncode(pathAndQuery))
	sb.WriteString(strconv.FormatInt(timestamp, 10))
	sb.WriteString(nonce)
	sb.WriteString(content)

	mac := hmac.New(sha256.New, []byte(apiSecret))
	mac.Write([]byte(sb.String()))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("hmac %s:%s:%s:%d", apiKey, signature, nonce, timestamp)
}

// urlEncode reproduces PHP's urlencode: letters, digits, '-', '_' and '.' are
// left alone, a space becomes '+', and everything else becomes an uppercase
// percent escape. Go's url.QueryEscape differs because it also leaves '~'
// unescaped, which would produce a signature the API rejects.
func urlEncode(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
			sb.WriteByte(c)
		case c == ' ':
			sb.WriteByte('+')
		default:
			sb.WriteByte('%')
			sb.WriteString(strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return sb.String()
}

func newNonce() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}
