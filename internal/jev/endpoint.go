package jev

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// EndpointOrigin reduces an endpoint to the destination identity a credential
// is bound to: scheme plus host, with default ports dropped. It rejects URL
// components commonly abused to smuggle credentials.
func EndpointOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return "", errors.New("jev endpoint must be an http or https URL with a host")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if parsed.Host == "" || (scheme != "http" && scheme != "https") {
		return "", errors.New("jev endpoint must be an http or https URL with a host")
	}
	if parsed.User != nil {
		return "", errors.New("jev endpoint must not contain credentials")
	}
	if parsed.RawQuery != "" {
		return "", errors.New("jev endpoint must not contain a query")
	}
	if parsed.Fragment != "" {
		return "", errors.New("jev endpoint must not contain a fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

func loopbackOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
