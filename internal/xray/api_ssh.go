package xray

import (
	"fmt"
	"strings"

	"github.com/xtls/xray-core/common/serial"
	coressh "github.com/xtls/xray-core/proxy/ssh"
	"golang.org/x/crypto/ssh"
)

func buildSSHUserAccount(user map[string]any) (*serial.TypedMessage, error) {
	username, err := getRequiredUserString(user, "username")
	if err != nil {
		return nil, err
	}
	password, err := getOptionalUserString(user, "password")
	if err != nil {
		return nil, err
	}
	var keys []string
	switch raw := user["publicKeys"].(type) {
	case nil:
	case []string:
		keys = raw
	case []any:
		for _, value := range raw {
			key, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("SSH publicKeys must contain strings")
			}
			keys = append(keys, key)
		}
	default:
		return nil, fmt.Errorf("SSH publicKeys must be a string array")
	}
	if username == "" || len(username) > 256 || len(password) > 1024 || len(keys) > 16 || len(keys) == 0 && password == "" {
		return nil, coressh.ErrConfiguration
	}
	for _, line := range keys {
		if len(line) > 16384 {
			return nil, coressh.ErrConfiguration
		}
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(options) != 0 || strings.TrimSpace(string(rest)) != "" {
			return nil, coressh.ErrConfiguration
		}
		if _, certificate := key.(*ssh.Certificate); certificate {
			return nil, coressh.ErrConfiguration
		}
	}
	return serial.ToTypedMessage(&coressh.Account{Username: username, Password: password, PublicKeys: keys}), nil
}
