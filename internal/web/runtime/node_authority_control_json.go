package runtime

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func nodeControlFields(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	if len(raw) > NodeAuthorityMessageLimit || !utf8.Valid(raw) {
		return nil, ErrNodeAuthorityDiscovery
	}
	fields, err := DecodeNodeAuthorityObject(bytes.NewReader(raw), allowed...)
	if err != nil || len(fields) != len(allowed) {
		return nil, ErrNodeAuthorityDiscovery
	}
	return fields, nil
}

func decodeNodeControlBinding(raw []byte, b *NodeAuthorityControlBinding) error {
	if _, err := nodeControlFields(raw, "expectedInstanceId", "expectedBootId", "authorityId", "generation", "nodeId"); err != nil {
		return err
	}
	type plain NodeAuthorityControlBinding
	if json.Unmarshal(raw, (*plain)(b)) != nil {
		return ErrNodeAuthorityDiscovery
	}
	return b.Validate()
}

// Only canonical protobuf JSON names and decimal-string uint64 are accepted.
// The core schema supplies the fields; duplicate/null/alias checks happen before
// protobuf decoding, which otherwise accepts several equivalent representations.
func checkNodeControlProto(raw []byte, descriptor protoreflect.MessageDescriptor) error {
	allowed := make([]string, descriptor.Fields().Len())
	for i := range allowed {
		allowed[i] = descriptor.Fields().Get(i).JSONName()
	}
	fields, err := nodeControlFields(raw, allowed...)
	if err != nil {
		return err
	}
	for i := 0; i < descriptor.Fields().Len(); i++ {
		field := descriptor.Fields().Get(i)
		value := fields[field.JSONName()]
		if field.IsList() {
			var entries []json.RawMessage
			if field.Kind() != protoreflect.MessageKind || json.Unmarshal(value, &entries) != nil || len(entries) > 128 {
				return ErrNodeAuthorityDiscovery
			}
			for _, entry := range entries {
				if err := checkNodeControlProto(entry, field.Message()); err != nil {
					return err
				}
			}
			continue
		}
		switch field.Kind() {
		case protoreflect.MessageKind:
			if err := checkNodeControlProto(value, field.Message()); err != nil {
				return err
			}
		case protoreflect.Uint64Kind:
			var text string
			if json.Unmarshal(value, &text) != nil {
				return ErrNodeAuthorityDiscovery
			}
			n, err := strconv.ParseUint(text, 10, 64)
			if err != nil || strconv.FormatUint(n, 10) != text {
				return ErrNodeAuthorityDiscovery
			}
		case protoreflect.Uint32Kind:
			var n uint32
			if json.Unmarshal(value, &n) != nil {
				return ErrNodeAuthorityDiscovery
			}
		case protoreflect.StringKind:
			var text string
			if json.Unmarshal(value, &text) != nil {
				return ErrNodeAuthorityDiscovery
			}
		case protoreflect.BoolKind:
			var flag bool
			if json.Unmarshal(value, &flag) != nil {
				return ErrNodeAuthorityDiscovery
			}
		default:
			return ErrNodeAuthorityDiscovery
		}
	}
	return nil
}

func decodeNodeControlProto(raw []byte, message proto.Message) error {
	if err := checkNodeControlProto(raw, message.ProtoReflect().Descriptor()); err != nil {
		return err
	}
	if (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, message) != nil {
		return ErrNodeAuthorityDiscovery
	}
	return nil
}

func marshalNodeControlProto(message proto.Message) (json.RawMessage, error) {
	if message == nil || !message.ProtoReflect().IsValid() {
		return nil, ErrNodeAuthorityDiscovery
	}
	raw, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(message)
	if err != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	if err := checkNodeControlProto(raw, message.ProtoReflect().Descriptor()); err != nil {
		return nil, err
	}
	return raw, nil
}

func marshalNodeControl(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > NodeAuthorityMessageLimit {
		return nil, ErrNodeAuthorityDiscovery
	}
	return raw, nil
}

func (r NodeAuthorityRequestsRequest) MarshalJSON() ([]byte, error) {
	if r.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	type plain NodeAuthorityRequestsRequest
	return marshalNodeControl(plain(r))
}
func (r *NodeAuthorityRequestsRequest) UnmarshalJSON(raw []byte) error {
	fields, err := nodeControlFields(raw, "binding", "limit")
	if err != nil {
		return err
	}
	var result NodeAuthorityRequestsRequest
	if decodeNodeControlBinding(fields["binding"], &result.Binding) != nil || json.Unmarshal(fields["limit"], &result.Limit) != nil || result.Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	*r = result
	return nil
}
func (r NodeAuthorityInstallRequest) MarshalJSON() ([]byte, error) {
	if r.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	grant, err := marshalNodeControlProto(r.Grant)
	if err != nil {
		return nil, err
	}
	return marshalNodeControl(struct {
		Binding NodeAuthorityControlBinding `json:"binding"`
		Grant   json.RawMessage             `json:"grant"`
	}{r.Binding, grant})
}
func (r *NodeAuthorityInstallRequest) UnmarshalJSON(raw []byte) error {
	fields, err := nodeControlFields(raw, "binding", "grant")
	if err != nil {
		return err
	}
	result := NodeAuthorityInstallRequest{Grant: &command.ExecutionGrant{}}
	if decodeNodeControlBinding(fields["binding"], &result.Binding) != nil || decodeNodeControlProto(fields["grant"], result.Grant) != nil || result.Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	*r = result
	return nil
}
func (r NodeAuthorityGrantRequest) MarshalJSON() ([]byte, error) {
	if r.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	type plain NodeAuthorityGrantRequest
	return marshalNodeControl(plain(r))
}
func (r *NodeAuthorityGrantRequest) UnmarshalJSON(raw []byte) error {
	fields, err := nodeControlFields(raw, "binding", "clientId", "grantId")
	if err != nil {
		return err
	}
	var result NodeAuthorityGrantRequest
	if decodeNodeControlBinding(fields["binding"], &result.Binding) != nil || json.Unmarshal(fields["clientId"], &result.ClientID) != nil || json.Unmarshal(fields["grantId"], &result.GrantID) != nil || result.Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	*r = result
	return nil
}
func (r NodeAuthorityRenewalRequest) MarshalJSON() ([]byte, error) {
	if r.Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	renewal, err := marshalNodeControlProto(r.Renewal)
	if err != nil {
		return nil, err
	}
	return marshalNodeControl(struct {
		Binding NodeAuthorityControlBinding `json:"binding"`
		Renewal json.RawMessage             `json:"renewal"`
	}{r.Binding, renewal})
}
func (r *NodeAuthorityRenewalRequest) UnmarshalJSON(raw []byte) error {
	fields, err := nodeControlFields(raw, "binding", "renewal")
	if err != nil {
		return err
	}
	result := NodeAuthorityRenewalRequest{Renewal: &command.AuthorityRenewalRequest{}}
	if decodeNodeControlBinding(fields["binding"], &result.Binding) != nil || decodeNodeControlProto(fields["renewal"], result.Renewal) != nil || result.Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	*r = result
	return nil
}

func (i NodeAuthorityControlIdentity) controlBinding() NodeAuthorityControlBinding {
	return NodeAuthorityControlBinding{ExpectedInstanceID: i.InstanceID, ExpectedBootID: i.BootID, AuthorityID: i.ExecutionRole.AuthorityID, Generation: i.ExecutionRole.Generation, NodeID: i.ExecutionRole.NodeID}
}
func decodeNodeControlIdentity(fields map[string]json.RawMessage, i *NodeAuthorityControlIdentity) error {
	if _, err := nodeControlFields(fields["executionRole"], "mode", "authorityId", "generation", "nodeId"); err != nil {
		return err
	}
	if json.Unmarshal(fields["instanceId"], &i.InstanceID) != nil || json.Unmarshal(fields["bootId"], &i.BootID) != nil || json.Unmarshal(fields["executionRole"], &i.ExecutionRole) != nil {
		return ErrNodeAuthorityDiscovery
	}
	return i.Validate(i.controlBinding())
}
func marshalNodeControlResult(identity NodeAuthorityControlIdentity, key string, message proto.Message) ([]byte, error) {
	if identity.Validate(identity.controlBinding()) != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	payload, err := marshalNodeControlProto(message)
	if err != nil {
		return nil, err
	}
	return marshalNodeControl(map[string]any{"instanceId": identity.InstanceID, "bootId": identity.BootID, "executionRole": identity.ExecutionRole, key: payload})
}
func (r NodeAuthorityRequestsResult) MarshalJSON() ([]byte, error) {
	if r.Validate(NodeAuthorityRequestsRequest{Binding: r.controlBinding(), Limit: 128}) != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	return marshalNodeControlResult(r.NodeAuthorityControlIdentity, "requests", r.Requests)
}
func (r *NodeAuthorityRequestsResult) UnmarshalJSON(raw []byte) error {
	fields, err := nodeControlFields(raw, "instanceId", "bootId", "executionRole", "requests")
	if err != nil {
		return err
	}
	result := NodeAuthorityRequestsResult{Requests: &command.AuthorityRequests{}}
	if decodeNodeControlIdentity(fields, &result.NodeAuthorityControlIdentity) != nil || decodeNodeControlProto(fields["requests"], result.Requests) != nil || result.Validate(NodeAuthorityRequestsRequest{Binding: result.controlBinding(), Limit: 128}) != nil {
		return ErrNodeAuthorityDiscovery
	}
	*r = result
	return nil
}
func (r NodeAuthorityGrantResult) MarshalJSON() ([]byte, error) {
	if r.State == nil || r.State.Grant == nil || r.Validate(NodeAuthorityGrantRequest{Binding: r.controlBinding(), ClientID: r.State.Grant.ClientId, GrantID: r.State.Grant.GrantId}) != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	return marshalNodeControlResult(r.NodeAuthorityControlIdentity, "state", r.State)
}
func (r *NodeAuthorityGrantResult) UnmarshalJSON(raw []byte) error {
	fields, err := nodeControlFields(raw, "instanceId", "bootId", "executionRole", "state")
	if err != nil {
		return err
	}
	result := NodeAuthorityGrantResult{State: &command.ExecutionGrantState{}}
	if decodeNodeControlIdentity(fields, &result.NodeAuthorityControlIdentity) != nil || decodeNodeControlProto(fields["state"], result.State) != nil || result.State.Grant == nil || result.Validate(NodeAuthorityGrantRequest{Binding: result.controlBinding(), ClientID: result.State.Grant.ClientId, GrantID: result.State.Grant.GrantId}) != nil {
		return ErrNodeAuthorityDiscovery
	}
	*r = result
	return nil
}
func (r NodeAuthorityRenewalResult) MarshalJSON() ([]byte, error) {
	if r.NodeAuthorityControlIdentity.Validate(r.controlBinding()) != nil || (NodeAuthorityRenewalRequest{Binding: r.controlBinding(), Renewal: r.Renewal}).Validate() != nil {
		return nil, ErrNodeAuthorityDiscovery
	}
	return marshalNodeControlResult(r.NodeAuthorityControlIdentity, "renewal", r.Renewal)
}
func (r *NodeAuthorityRenewalResult) UnmarshalJSON(raw []byte) error {
	fields, err := nodeControlFields(raw, "instanceId", "bootId", "executionRole", "renewal")
	if err != nil {
		return err
	}
	result := NodeAuthorityRenewalResult{Renewal: &command.AuthorityRenewalRequest{}}
	if decodeNodeControlIdentity(fields, &result.NodeAuthorityControlIdentity) != nil || decodeNodeControlProto(fields["renewal"], result.Renewal) != nil || (NodeAuthorityRenewalRequest{Binding: result.controlBinding(), Renewal: result.Renewal}).Validate() != nil {
		return ErrNodeAuthorityDiscovery
	}
	*r = result
	return nil
}
