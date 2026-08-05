package fzfsidecar

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

func loopbackPort(listener net.Listener) (int, error) {
	if listener == nil {
		return 0, errors.New("fzf sidecar: reservation is not numeric IPv4 loopback")
	}
	listenerAddress := listener.Addr()
	if listenerAddress == nil {
		return 0, errors.New("fzf sidecar: reservation is not numeric IPv4 loopback")
	}
	if address, ok := listenerAddress.(*net.TCPAddr); ok {
		if address.Port > 0 && address.Port <= 65535 && address.IP.To4() != nil && address.IP.Equal(net.IPv4(127, 0, 0, 1)) {
			return address.Port, nil
		}
		return 0, errors.New("fzf sidecar: reservation is not numeric IPv4 loopback")
	}
	host, portText, err := net.SplitHostPort(listenerAddress.String())
	ip := net.ParseIP(host)
	if err != nil || strings.Contains(host, ":") || ip == nil || ip.To4() == nil || !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		return 0, errors.New("fzf sidecar: reservation is not numeric IPv4 loopback")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return 0, errors.New("fzf sidecar: reservation is not numeric IPv4 loopback")
	}
	return port, nil
}
