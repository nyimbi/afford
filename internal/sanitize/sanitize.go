// Package sanitize removes credentials from URLs before they are written to a
// report, a log or a tool result.
package sanitize

import (
	"net/url"
	"regexp"
)

var secretKey = regexp.MustCompile(`(?i)token|key|secret|password|passwd|auth|session|code|signature|sig$`)

// URL masks the values of query parameters whose names look like credentials.
// Path and host are preserved so the reader can still tell what was called.
func URL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RawQuery == "" {
		return raw
	}
	query := parsed.Query()
	changed := false
	for key := range query {
		if secretKey.MatchString(key) {
			query.Set(key, "***")
			changed = true
		}
	}
	if !changed {
		return raw
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
