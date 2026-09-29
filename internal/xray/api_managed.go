package xray

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	policycommand "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy/dokodemo"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func (x *XrayAPI) requireManagedControl(ctx context.Context, required []string) error {
	if !filepath.IsAbs(x.endpoint) || x.grpcClient == nil {
		return fmt.Errorf("%w: managed identities require private control", ErrClientPolicyCapability)
	}
	client := policycommand.NewClientPolicyServiceClient(x.grpcClient)
	caps, err := client.GetCapabilities(ctx, &policycommand.Empty{})
	if err != nil {
		return fmt.Errorf("%w: cannot verify managed control: %w", ErrClientPolicyCapability, err)
	}
	if err := validateClientPolicyCapabilities(caps, caps.GetInstanceId()); err != nil {
		return err
	}
	for _, name := range required {
		if !slices.Contains(caps.Capabilities, name) {
			return fmt.Errorf("%w: missing %s", ErrClientPolicyCapability, name)
		}
	}
	return nil
}

func managedUserCapabilities(accountType string) ([]string, error) {
	var capability string
	switch accountType {
	case "xray.proxy.vless.Account":
		capability = "trusted-vless-client-id-v1"
	case "xray.proxy.vmess.Account":
		capability = "trusted-vmess-client-id-v1"
	case "xray.proxy.trojan.Account":
		capability = "trusted-trojan-client-id-v1"
	case legacyShadowsocksAccountType:
		capability = "trusted-shadowsocks-aead-client-id-v1"
	default:
		return nil, fmt.Errorf("%w: managed account adapter is not implemented for %q", ErrClientPolicyCapability, accountType)
	}
	return []string{capability, "authenticated-credential-revocation-v1", "inbound-scoped-session-close-v1"}, nil
}

func managedIdentityCapabilities(message protoreflect.Message, required map[string]bool) error {
	switch value := message.Interface().(type) {
	case *protocol.User:
		if value.GetClientId() != "" {
			capabilities, err := managedUserCapabilities(value.Account.GetType())
			if err != nil {
				return err
			}
			for _, capability := range capabilities {
				required[capability] = true
			}
		}
		return nil
	case *dokodemo.Config:
		if value.GetClientId() != "" {
			required["trusted-tunnel-client-id-v1"] = true
		}
		return nil
	}
	var err error
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.IsMap():
			if field.MapValue().Kind() == protoreflect.MessageKind {
				value.Map().Range(func(_ protoreflect.MapKey, entry protoreflect.Value) bool {
					err = managedIdentityCapabilities(entry.Message(), required)
					return err == nil
				})
			}
		case field.Kind() == protoreflect.MessageKind:
			if field.IsList() {
				list := value.List()
				for i := 0; i < list.Len() && err == nil; i++ {
					err = managedIdentityCapabilities(list.Get(i).Message(), required)
				}
			} else {
				err = managedIdentityCapabilities(value.Message(), required)
			}
		}
		return err == nil
	})
	return err
}
