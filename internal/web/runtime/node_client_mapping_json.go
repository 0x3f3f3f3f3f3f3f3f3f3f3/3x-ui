package runtime

import (
	"encoding/json"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func mappingInteger(raw []byte, value *uint64) error {
	var decimal string
	if json.Unmarshal(raw, &decimal) != nil {
		return ErrNodeAuthorityDiscovery
	}
	n, err := strconv.ParseUint(decimal, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != decimal {
		return ErrNodeAuthorityDiscovery
	}
	*value = n
	return nil
}

func (r NodeClientMappingRequest) MarshalJSON() ([]byte, error) {
	if r.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	type plain NodeClientMappingRequest
	return marshalNodeControl(plain(r))
}

func (r *NodeClientMappingRequest) UnmarshalJSON(raw []byte) error {
	fields, err := nodeControlFields(raw, "binding", "globalClientId", "localClientId", "globalPolicyVersion", "localPolicyVersion", "expectedPolicyDigest")
	if err != nil {
		return err
	}
	var result NodeClientMappingRequest
	if decodeNodeControlBinding(fields["binding"], &result.Binding) != nil ||
		json.Unmarshal(fields["globalClientId"], &result.GlobalClientID) != nil ||
		json.Unmarshal(fields["localClientId"], &result.LocalClientID) != nil ||
		mappingInteger(fields["globalPolicyVersion"], &result.GlobalPolicyVersion) != nil ||
		mappingInteger(fields["localPolicyVersion"], &result.LocalPolicyVersion) != nil ||
		json.Unmarshal(fields["expectedPolicyDigest"], &result.ExpectedPolicyDigest) != nil || result.Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	*r = result
	return nil
}

type mappingWireIdentity struct {
	AuthorityID string `json:"authorityId"`
	Generation  uint64 `json:"generation,string"`
}

type mappingWireEvidence struct {
	Authority           mappingWireIdentity `json:"authority"`
	NodeAnchor          mappingWireIdentity `json:"nodeAnchor"`
	NodeID              string              `json:"nodeId"`
	SourceID            string              `json:"sourceId"`
	GlobalClientID      string              `json:"globalClientId"`
	LocalClientID       string              `json:"localClientId"`
	GlobalPolicyVersion uint64              `json:"globalPolicyVersion,string"`
	LocalPolicyVersion  uint64              `json:"localPolicyVersion,string"`
	PolicyDigest        string              `json:"policyDigest"`
}

func decodeMappingIdentity(raw []byte, identity *policyauthority.Identity) error {
	fields, err := nodeControlFields(raw, "authorityId", "generation")
	if err != nil {
		return err
	}
	if json.Unmarshal(fields["authorityId"], &identity.AuthorityID) != nil || mappingInteger(fields["generation"], &identity.Generation) != nil {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func (r NodeClientMappingResult) ownRequest() NodeClientMappingRequest {
	m := r.Mapping
	return NodeClientMappingRequest{Binding: r.controlBinding(), GlobalClientID: m.GlobalClientID, LocalClientID: m.LocalClientID, GlobalPolicyVersion: m.GlobalPolicyVersion, LocalPolicyVersion: m.LocalPolicyVersion, ExpectedPolicyDigest: m.PolicyDigest}
}

func (r NodeClientMappingResult) MarshalJSON() ([]byte, error) {
	if r.Validate(r.ownRequest()) != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	m := r.Mapping
	wire := mappingWireEvidence{Authority: mappingWireIdentity{m.Authority.AuthorityID, m.Authority.Generation}, NodeAnchor: mappingWireIdentity{m.NodeAnchor.AuthorityID, m.NodeAnchor.Generation}, NodeID: m.NodeID, SourceID: m.SourceID, GlobalClientID: m.GlobalClientID, LocalClientID: m.LocalClientID, GlobalPolicyVersion: m.GlobalPolicyVersion, LocalPolicyVersion: m.LocalPolicyVersion, PolicyDigest: m.PolicyDigest}
	return marshalNodeControl(struct {
		NodeAuthorityControlIdentity
		Mapping mappingWireEvidence `json:"mapping"`
	}{r.NodeAuthorityControlIdentity, wire})
}

func (r *NodeClientMappingResult) UnmarshalJSON(raw []byte) error {
	fields, err := nodeControlFields(raw, "instanceId", "bootId", "executionRole", "mapping")
	if err != nil {
		return err
	}
	var result NodeClientMappingResult
	if decodeNodeControlIdentity(fields, &result.NodeAuthorityControlIdentity) != nil {
		return ErrNodeAuthorityDiscovery
	}
	evidence, err := nodeControlFields(fields["mapping"], "authority", "nodeAnchor", "nodeId", "sourceId", "globalClientId", "localClientId", "globalPolicyVersion", "localPolicyVersion", "policyDigest")
	if err != nil {
		return err
	}
	m := &result.Mapping
	if decodeMappingIdentity(evidence["authority"], &m.Authority) != nil || decodeMappingIdentity(evidence["nodeAnchor"], &m.NodeAnchor) != nil ||
		json.Unmarshal(evidence["nodeId"], &m.NodeID) != nil || json.Unmarshal(evidence["sourceId"], &m.SourceID) != nil ||
		json.Unmarshal(evidence["globalClientId"], &m.GlobalClientID) != nil || json.Unmarshal(evidence["localClientId"], &m.LocalClientID) != nil ||
		mappingInteger(evidence["globalPolicyVersion"], &m.GlobalPolicyVersion) != nil || mappingInteger(evidence["localPolicyVersion"], &m.LocalPolicyVersion) != nil ||
		json.Unmarshal(evidence["policyDigest"], &m.PolicyDigest) != nil || result.Validate(result.ownRequest()) != nil {
		return ErrNodeAuthorityDiscovery
	}
	*r = result
	return nil
}
