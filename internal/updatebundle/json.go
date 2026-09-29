package updatebundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func uniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := uniqueJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after release manifest")
	}
	root, err := exactJSONObject(data, "schema", "identity", "policyABI", "routingABI", "files")
	if err != nil {
		return err
	}
	if _, err := exactJSONObject(root["identity"], "repository", "commit", "tag", "platform"); err != nil {
		return err
	}
	var files map[string]json.RawMessage
	if err := json.Unmarshal(root["files"], &files); err != nil {
		return err
	}
	for _, value := range files {
		if _, err := exactJSONObject(value, "sha256", "size", "executable"); err != nil {
			return err
		}
	}
	return nil
}

func exactJSONObject(data []byte, names ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if len(object) != len(names) {
		return nil, errors.New("release manifest contains missing or unexpected fields")
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return nil, fmt.Errorf("release manifest is missing exact field %q", name)
		}
	}
	return object, nil
}

func uniqueJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("release manifest nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("release manifest fields cannot be null")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' {
		return errors.New("release manifest requires objects and scalar values")
	}
	keys := make(map[string]bool)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || keys[name] {
			return fmt.Errorf("duplicate or invalid release manifest field: %q", name)
		}
		keys[name] = true
		if err := uniqueJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
