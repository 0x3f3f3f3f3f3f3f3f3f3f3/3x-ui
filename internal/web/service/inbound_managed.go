package service

import (
	"net"
	"strings"
)

func canonicalManagedListen(address string) (string, bool) {
	if address == "" {
		return "", true
	}
	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		address = address[1 : len(address)-1]
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return "", false
	}
	return ip.String(), true
}
