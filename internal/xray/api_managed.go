package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	policycommand "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/dokodemo"
	corehttp "github.com/xtls/xray-core/proxy/http"
	"github.com/xtls/xray-core/proxy/mieru"
	"github.com/xtls/xray-core/proxy/snell"
	"github.com/xtls/xray-core/proxy/socks"
	coressh "github.com/xtls/xray-core/proxy/ssh"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ManagedHotDiffCapabilities checks the same account adapters used by handler RPCs before preparation.
func ManagedHotDiffCapabilities(diff *HotDiff) ([]string, error) {
	required := make(map[string]bool)
	for _, protocol := range diff.RemovedInboundProtocols {
		if protocol == "ssh" || protocol == "snell" {
			for _, name := range []string{"trusted-" + protocol + "-client-id-v1", "authenticated-credential-revocation-v1", "inbound-scoped-session-close-v1"} {
				required[name] = true
			}
		}
	}
	if len(diff.RemovedUsers) > 0 {
		required["inbound-scoped-session-close-v1"] = true
		required["authenticated-credential-revocation-v1"] = true
	}
	for _, user := range diff.RemovedUsers {
		var accountType string
		switch user.Protocol {
		case "snell":
			accountType = "xray.proxy.snell.Account"
		case "ssh":
			accountType = "xray.proxy.ssh.Account"
		case "mieru":
			accountType = "xray.proxy.mieru.Account"
		case "socks", "mixed":
			accountType = "xray.proxy.socks.Account"
		case "http":
			accountType = "xray.proxy.http.Account"
		default:
			continue
		}
		capabilities, err := managedUserCapabilities(accountType)
		if err != nil {
			return nil, err
		}
		for _, capability := range capabilities {
			required[capability] = true
		}
	}
	for _, inbound := range diff.AddedInbounds {
		var config conf.InboundDetourConfig
		if err := json.Unmarshal(inbound, &config); err != nil {
			return nil, err
		}
		compiled, err := config.Build()
		if err != nil {
			return nil, err
		}
		proxy, err := compiled.ProxySettings.GetInstance()
		if err != nil {
			return nil, err
		}
		if err := managedIdentityCapabilities(proxy.ProtoReflect(), required); err != nil {
			return nil, err
		}
	}
	for _, user := range diff.AddedUsers {
		id, _ := user.User["clientId"].(string)
		if id == "" {
			continue
		}
		account, err := buildUserAccount(user.Protocol, user.User)
		if err != nil {
			return nil, err
		}
		if err := managedIdentityCapabilities((&protocol.User{ClientId: id, Account: account}).ProtoReflect(), required); err != nil {
			return nil, err
		}
	}
	return slices.Sorted(maps.Keys(required)), nil
}

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
	case "xray.proxy.snell.Account":
		capability = "trusted-snell-client-id-v1"
	case "xray.proxy.ssh.Account":
		capability = "trusted-ssh-client-id-v1"
	case "xray.proxy.mieru.Account":
		capability = "trusted-mieru-client-id-v1"
	case "xray.proxy.vless.Account":
		capability = "trusted-vless-client-id-v1"
	case "xray.proxy.vmess.Account":
		capability = "trusted-vmess-client-id-v1"
	case "xray.proxy.trojan.Account":
		capability = "trusted-trojan-client-id-v1"
	case legacyShadowsocksAccountType:
		capability = "trusted-shadowsocks-aead-client-id-v1"
	case "xray.proxy.http.Account":
		capability = "trusted-http-client-id-v1"
	case "xray.proxy.socks.Account":
		// SOCKS listeners also accept HTTP using the same credentials.
		return []string{"trusted-socks-client-id-v1", "trusted-http-client-id-v1", "authenticated-credential-revocation-v1", "inbound-scoped-session-close-v1"}, nil
	default:
		return nil, fmt.Errorf("%w: managed account adapter is not implemented for %q", ErrClientPolicyCapability, accountType)
	}
	return []string{capability, "authenticated-credential-revocation-v1", "inbound-scoped-session-close-v1"}, nil
}

func managedIdentityCapabilities(message protoreflect.Message, required map[string]bool) error {
	switch value := message.Interface().(type) {
	case *snell.ServerConfig:
		required["trusted-snell-client-id-v1"] = true
		required["authenticated-credential-revocation-v1"] = true
		required["inbound-scoped-session-close-v1"] = true
		return nil
	case *coressh.ServerConfig:
		required["trusted-ssh-client-id-v1"] = true
		required["authenticated-credential-revocation-v1"] = true
		required["inbound-scoped-session-close-v1"] = true
		return nil
	case *mieru.ServerConfig:
		required["trusted-mieru-client-id-v1"] = true
		required["authenticated-credential-revocation-v1"] = true
		required["inbound-scoped-session-close-v1"] = true
		return nil
	case *socks.ServerConfig:
		for _, id := range value.ClientIds {
			if id != "" {
				required["trusted-socks-client-id-v1"] = true
				required["trusted-http-client-id-v1"] = true
				required["authenticated-credential-revocation-v1"] = true
				required["inbound-scoped-session-close-v1"] = true
			}
		}
		if value.AuthType == socks.AuthType_PASSWORD && len(value.Accounts) == 0 {
			required["trusted-http-client-id-v1"] = true
		}
		return nil
	case *corehttp.ServerConfig:
		for _, id := range value.ClientIds {
			if id != "" {
				required["trusted-http-client-id-v1"] = true
				required["authenticated-credential-revocation-v1"] = true
				required["inbound-scoped-session-close-v1"] = true
			}
		}
		if value.RequireAuthentication {
			required["trusted-http-client-id-v1"] = true
		}
		return nil
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
		if value.GetOutboundTag() != "" {
			required["tunnel-fixed-outbound-v1"] = true
		}
		if len(value.GetAllowedSourceCidrs()) > 0 {
			required["tunnel-source-acl-v1"] = true
		}
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
