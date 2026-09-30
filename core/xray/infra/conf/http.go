package conf

import (
	"encoding/json"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/http"
	"google.golang.org/protobuf/proto"
)

type HTTPAccount struct {
	Username string `json:"user"`
	Password string `json:"pass"`
	ClientID string `json:"clientId"`
	Email    string `json:"email"`
}

func (v *HTTPAccount) Build() *http.Account {
	return &http.Account{
		Username: v.Username,
		Password: v.Password,
	}
}

type HTTPServerConfig struct {
	Users                 []*HTTPAccount `json:"users"`
	Accounts              []*HTTPAccount `json:"accounts"`
	Transparent           bool           `json:"allowTransparent"`
	UserLevel             uint32         `json:"userLevel"`
	RequireAuthentication bool           `json:"requireAuthentication"`
}

func (c *HTTPServerConfig) Build() (proto.Message, error) {
	config := &http.ServerConfig{
		AllowTransparent:      c.Transparent,
		UserLevel:             c.UserLevel,
		RequireAuthentication: c.RequireAuthentication,
	}

	if c.Accounts != nil {
		c.Users = c.Accounts
	}
	entries := make([]passwordIdentityAccount, 0, len(c.Users))
	for _, account := range c.Users {
		if account == nil {
			return nil, errors.New("HTTP account must not be null")
		}
		entries = append(entries, passwordIdentityAccount{account.Username, account.Password, account.ClientID, account.Email})
	}
	var err error
	config.Accounts, config.ClientIds, config.AccountEmails, err = buildPasswordIdentityAccounts(entries)
	if err != nil {
		return nil, err
	}

	return config, nil
}

type HTTPRemoteConfig struct {
	Address *Address          `json:"address"`
	Port    uint16            `json:"port"`
	Users   []json.RawMessage `json:"users"`
}

type HTTPClientConfig struct {
	Address  *Address            `json:"address"`
	Port     uint16              `json:"port"`
	Level    uint32              `json:"level"`
	Email    string              `json:"email"`
	Username string              `json:"user"`
	Password string              `json:"pass"`
	Servers  []*HTTPRemoteConfig `json:"servers"`
	Headers  map[string]string   `json:"headers"`
}

func (v *HTTPClientConfig) Build() (proto.Message, error) {
	config := new(http.ClientConfig)
	if v.Address != nil {
		v.Servers = []*HTTPRemoteConfig{
			{
				Address: v.Address,
				Port:    v.Port,
			},
		}
		if len(v.Username) > 0 {
			v.Servers[0].Users = []json.RawMessage{{}}
		}
	}
	if len(v.Servers) != 1 {
		return nil, errors.New(`HTTP settings: "servers" should have one and only one member. Multiple endpoints in "servers" should use multiple HTTP outbounds and routing balancer instead`)
	}
	for _, serverConfig := range v.Servers {
		if len(serverConfig.Users) > 1 {
			return nil, errors.New(`HTTP servers: "users" should have one member at most. Multiple members in "users" should use multiple HTTP outbounds and routing balancer instead`)
		}
		server := &protocol.ServerEndpoint{
			Address: serverConfig.Address.Build(),
			Port:    uint32(serverConfig.Port),
		}
		for _, rawUser := range serverConfig.Users {
			user := new(protocol.User)
			if v.Address != nil {
				user.Level = v.Level
				user.Email = v.Email
			} else {
				if err := json.Unmarshal(rawUser, user); err != nil {
					return nil, errors.New("failed to parse HTTP user").Base(err).AtError()
				}
			}
			account := new(HTTPAccount)
			if v.Address != nil {
				account.Username = v.Username
				account.Password = v.Password
			} else {
				if err := json.Unmarshal(rawUser, account); err != nil {
					return nil, errors.New("failed to parse HTTP account").Base(err).AtError()
				}
			}
			user.Account = serial.ToTypedMessage(account.Build())
			server.User = user
			break
		}
		config.Server = server
		break
	}
	config.Header = make([]*http.Header, 0, 32)
	for key, value := range v.Headers {
		config.Header = append(config.Header, &http.Header{
			Key:   key,
			Value: value,
		})
	}
	return config, nil
}
