package conf

import "errors"

type passwordIdentityAccount struct {
	username, password, clientID, email string
}

func buildPasswordIdentityAccounts(entries []passwordIdentityAccount) (accounts, clientIDs, emails map[string]string, err error) {
	if len(entries) == 0 {
		return nil, nil, nil, nil
	}
	managed := false
	for _, entry := range entries {
		managed = managed || entry.clientID != ""
	}
	accounts = make(map[string]string, len(entries))
	for _, entry := range entries {
		if _, exists := accounts[entry.username]; exists && managed {
			return nil, nil, nil, errors.New("managed password usernames must be unique")
		}
		accounts[entry.username] = entry.password
		if entry.clientID != "" {
			if clientIDs == nil {
				clientIDs = make(map[string]string)
			}
			clientIDs[entry.username] = entry.clientID
			if entry.email != "" {
				if emails == nil {
					emails = make(map[string]string)
				}
				emails[entry.username] = entry.email
			}
		}
	}
	return accounts, clientIDs, emails, nil
}
