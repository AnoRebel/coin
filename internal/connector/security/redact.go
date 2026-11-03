package security

import (
	"net/url"
	"sort"
	"strings"
)

// Redacted replaces a secret in a log-safe view.
const Redacted = "REDACTED"

var sensitiveQuery = map[string]bool{
	"token": true, "access_token": true, "api_key": true, "apikey": true,
	"key": true, "secret": true, "client_secret": true, "password": true,
	"passwd": true, "sig": true, "signature": true, "auth": true,
}

var sensitiveHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true, "cookie": true,
	"set-cookie": true, "x-api-key": true, "x-arne-signature": true,
	"x-auth-token": true,
}

// RedactURL removes credentials and redacts sensitive query values so an
// endpoint can be logged. A URL that does not parse is never echoed back,
// because it might itself contain a secret.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[invalid-url]"
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(parsed.Scheme))
	b.WriteString("://")
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		b.WriteString("[" + host + "]")
	} else {
		b.WriteString(host)
	}
	if port := parsed.Port(); port != "" {
		b.WriteString(":")
		b.WriteString(port)
	}
	b.WriteString(parsed.EscapedPath())

	query := parsed.Query()
	if len(query) > 0 {
		keys := make([]string, 0, len(query))
		for key := range query {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, key := range keys {
			value := Redacted
			if !sensitiveQuery[strings.ToLower(key)] {
				value = strings.Join(query[key], ",")
			}
			pairs = append(pairs, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
		b.WriteString("?")
		b.WriteString(strings.Join(pairs, "&"))
	}
	return b.String()
}

// RedactHeaders returns a copy with authentication headers replaced. Key casing
// is preserved so the log still shows which header was present.
func RedactHeaders(headers map[string]string) map[string]string {
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		if sensitiveHeaders[strings.ToLower(key)] {
			out[key] = Redacted
			continue
		}
		out[key] = value
	}
	return out
}

// RedactAccount keeps at most the last four characters of an account number,
// and hides short values entirely because four of five characters is not a
// redaction.
func RedactAccount(account string) string {
	if len(account) < 8 {
		return "****"
	}
	return "****" + account[len(account)-4:]
}
