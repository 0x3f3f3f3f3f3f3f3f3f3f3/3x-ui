package runtime

import (
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
)

const NodeAuthorityMessageLimit = 32 << 10

var ErrNodeAuthorityDiscovery = errors.New("invalid or unavailable node authority discovery")

type AuthorityDiscoveryRequest struct {
	ExpectedInstanceID string `json:"expectedInstanceId"`
	ExpectedBootID     string `json:"expectedBootId"`
}

type NodeAuthorityDiscovery struct {
	Capabilities *command.Capabilities       `json:"capabilities"`
	Challenge    *command.AuthorityChallenge `json:"challenge"`
}

func authorityNonce(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func authorityInstance(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, " \t\r\n\x00")
}

func (r AuthorityDiscoveryRequest) Validate() error {
	if r.ExpectedInstanceID == "" && r.ExpectedBootID == "" {
		return nil
	}
	if !authorityInstance(r.ExpectedInstanceID) || !authorityNonce(r.ExpectedBootID) {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func (d *NodeAuthorityDiscovery) Validate(request AuthorityDiscoveryRequest) error {
	if request.Validate() != nil || d == nil || d.Capabilities == nil || d.Challenge == nil {
		return ErrNodeAuthorityDiscovery
	}
	c, challenge := d.Capabilities, d.Challenge
	if c.ApiVersion != 1 || !authorityInstance(c.InstanceId) || !authorityNonce(c.BootId) || len(c.Capabilities) > 64 || challenge.InstanceId != c.InstanceId || challenge.BootId != c.BootId || !authorityNonce(challenge.ChallengeId) || challenge.MaxDurationMillis == 0 || challenge.MaxDurationMillis > 10000 {
		return ErrNodeAuthorityDiscovery
	}
	if request.ExpectedInstanceID != "" && (c.InstanceId != request.ExpectedInstanceID || c.BootId != request.ExpectedBootID) {
		return ErrNodeAuthorityDiscovery
	}
	for _, name := range c.Capabilities {
		if name == "" || len(name) > 128 {
			return ErrNodeAuthorityDiscovery
		}
	}
	for _, required := range []string{"fresh-core-incarnation-v1", "monotonic-authority-challenge-v1", "boot-bound-execution-grants-v1"} {
		if !slices.Contains(c.Capabilities, required) {
			return ErrNodeAuthorityDiscovery
		}
	}
	return nil
}
