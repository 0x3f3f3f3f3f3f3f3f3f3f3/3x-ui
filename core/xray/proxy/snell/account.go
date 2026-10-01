package snell

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"google.golang.org/protobuf/proto"
)

type MemoryAccount struct{ PSK string }

func (a *MemoryAccount) Equals(other protocol.Account) bool {
	b, ok := other.(*MemoryAccount)
	return ok && a.PSK == b.PSK
}
func (a *MemoryAccount) ToProto() proto.Message { return &Account{Psk: a.PSK} }
func (a *Account) AsAccount() (protocol.Account, error) {
	if a.Psk == "" {
		return nil, errors.New("Snell requires a PSK")
	}
	return &MemoryAccount{PSK: a.Psk}, nil
}

func validate(version uint32, psk, obfs, mode string, quic bool) error {
	if version < 4 || version > 6 {
		return errors.New("Snell version must be explicitly 4, 5 or 6")
	}
	if psk == "" || len(psk) > 255 || version == 6 && len(psk) < 12 {
		return errors.New("invalid Snell PSK length")
	}
	if quic {
		return errors.New("Snell v5 QUIC Proxy Mode is not implemented")
	}
	if version == 6 {
		if obfs != "" && obfs != "off" {
			return errors.New("Snell v6 does not support legacy obfs")
		}
		if mode != "" && mode != "default" && mode != "unshaped" {
			return errors.New("unsupported Snell v6 mode; unsafe-raw is not authenticated")
		}
	} else {
		if mode != "" {
			return errors.New("Snell mode is only valid for v6")
		}
		if obfs != "" && obfs != "off" && obfs != "http" {
			return errors.New("Snell v4/v5 only support off or http obfs")
		}
	}
	return nil
}

func validateUser(user *protocol.MemoryUser) error {
	if user == nil || user.Email == "" {
		return errors.New("Snell requires a canonical listener owner and email")
	}
	if id, err := uuid.Parse(user.ClientID); err != nil || id.String() != user.ClientID {
		return errors.New("Snell listener owner must be a canonical UUID")
	}
	a, ok := user.Account.(*MemoryAccount)
	if !ok || a.PSK == "" {
		return errors.New("Snell owner requires a typed PSK account")
	}
	return nil
}

func ValidateServer(config *ServerConfig) error {
	if config == nil || config.User == nil {
		return errors.New("Snell requires one exclusive listener owner")
	}
	u, err := config.User.ToMemoryUser()
	if err != nil {
		return err
	}
	if err = validateUser(u); err != nil {
		return err
	}
	return validate(config.Version, u.Account.(*MemoryAccount).PSK, config.Obfs, config.Mode, config.Quic)
}

func ValidateClient(config *ClientConfig) error {
	if config == nil || config.Address == nil || config.Port == 0 || config.Port > 65535 {
		return errors.New("Snell outbound requires an address and valid port")
	}
	switch address := config.Address.Address.(type) {
	case *X.IPOrDomain_Domain:
		if address == nil || address.Domain == "" || strings.TrimSpace(address.Domain) != address.Domain {
			return errors.New("Snell outbound requires a valid server domain")
		}
	case *X.IPOrDomain_Ip:
		if address == nil || len(address.Ip) != 4 && len(address.Ip) != 16 {
			return errors.New("Snell outbound requires a valid server IP")
		}
	default:
		return errors.New("Snell outbound address type is missing")
	}
	return validate(config.Version, config.Psk, config.Obfs, config.Mode, config.Quic)
}
