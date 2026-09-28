package model

type SSHClient struct {
	PublicKeys []string        `json:"publicKeys"`
	Targets    []SSHTarget     `json:"targets"`
	Reverse    []SSHRemoteBind `json:"reverse,omitempty"`
}

type SSHTarget struct {
	Host string `json:"host"`
	Port uint16 `json:"port"`
}

type SSHRemoteBind struct {
	Address string `json:"address"`
	Port    uint16 `json:"port"`
}
