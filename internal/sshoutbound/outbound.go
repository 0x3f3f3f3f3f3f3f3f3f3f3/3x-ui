package sshoutbound

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

const MaxOutbounds = 32

type Outbound struct {
	Tag      string `json:"tag"`
	Settings Config `json:"settings"`
}

func validateTag(tag string) error {
	if tag == "" || len(tag) > 128 || strings.TrimSpace(tag) != tag || strings.ContainsFunc(tag, unicode.IsControl) {
		return fmt.Errorf("%w: invalid routing tag", ErrConfig)
	}
	return nil
}

func ParseOutbounds(raws []json.RawMessage) ([]Outbound, error) {
	tags := make(map[string]int)
	var desired []Outbound
	for _, raw := range raws {
		var meta struct {
			Protocol string `json:"protocol"`
			Tag      string `json:"tag"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("%w: unreadable outbound", ErrConfig)
		}
		tags[meta.Tag]++
		if !strings.EqualFold(meta.Protocol, "ssh") {
			continue
		}
		var authored struct {
			Protocol string `json:"protocol"`
			Outbound
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&authored); err != nil {
			return nil, fmt.Errorf("%w: unsupported or malformed SSH outbound fields", ErrConfig)
		}
		if err := validateTag(authored.Tag); err != nil {
			return nil, err
		}
		if _, _, err := compileConfig(authored.Settings); err != nil {
			return nil, err
		}
		desired = append(desired, authored.Outbound)
		if len(desired) > MaxOutbounds {
			return nil, fmt.Errorf("%w: too many SSH outbounds", ErrConfig)
		}
	}
	for _, outbound := range desired {
		if tags[outbound.Tag] != 1 {
			return nil, fmt.Errorf("%w: duplicate SSH routing tag", ErrConfig)
		}
	}
	return desired, nil
}
