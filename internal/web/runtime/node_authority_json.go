package runtime

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
)

// DecodeNodeAuthorityObject requires unique, exact field names and non-null
// values. The caller supplies a bounded body and validates each field's type.
func DecodeNodeAuthorityObject(body io.Reader, allowed ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(body)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrNodeAuthorityDiscovery
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !slices.Contains(allowed, key) || fields[key] != nil {
			return nil, ErrNodeAuthorityDiscovery
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, ErrNodeAuthorityDiscovery
		}
		fields[key] = raw
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrNodeAuthorityDiscovery
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrNodeAuthorityDiscovery
	}
	return fields, nil
}
