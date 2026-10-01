package mieru

import (
	"errors"
	"unicode/utf8"

	"github.com/xtls/xray-core/common/protocol"
	"google.golang.org/protobuf/proto"
)

func ValidateCredentials(username, password string) error {
	if username == "" || password == "" || len(username) > 64 || len(password) > 64 || !utf8.ValidString(username) || !utf8.ValidString(password) {
		return errors.New("mieru requires valid nonempty username/password up to 64 bytes")
	}
	return nil
}

func (a *Account) Equals(other protocol.Account) bool {
	value, ok := other.(*Account)
	return ok && a.Username == value.Username && a.Password == value.Password
}

func (a *Account) ToProto() proto.Message { return a }

func (a *Account) AsAccount() (protocol.Account, error) {
	if err := ValidateCredentials(a.Username, a.Password); err != nil {
		return nil, err
	}
	return a, nil
}
