package conf

import (
	"encoding/json"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/protocol"
)

func parseInboundUser(raw json.RawMessage) (*protocol.User, error) {
	u := struct {
		*protocol.User
		ClientID *string `json:"clientId"`
	}{User: new(protocol.User)}
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, err
	}
	if u.ClientID != nil {
		if u.User.ClientId != "" && u.User.ClientId != *u.ClientID {
			return nil, errors.New("conflicting clientId and client_id")
		}
		u.User.ClientId = *u.ClientID
	}
	return u.User, nil
}
