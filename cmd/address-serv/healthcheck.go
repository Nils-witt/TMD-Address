package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// healthcheck asks the server listening on LISTEN_ADDR for GET /api/version
// and returns nil on 200. The image has no shell or HTTP client, so its
// HEALTHCHECK runs `address-serv healthcheck`. /api/version is used rather
// than /healthz because it needs no token.
func healthcheck() error {
	url := "http://" + loopbackAddr(getenvDefault("LISTEN_ADDR", defaultListenAddr)) + "/api/version"

	client := &http.Client{Timeout: 3 * time.Second}

	resp, err := client.Get(url) //nolint:noctx // one-shot probe, bounded by the client timeout
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s returned %s", url, resp.Status)
	}

	return nil
}

// loopbackAddr turns a listen address into one to dial: an empty or
// unspecified host (":8080", "0.0.0.0:8080", "[::]:8080") becomes 127.0.0.1.
func loopbackAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}

	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}

	return net.JoinHostPort(host, port)
}

func runHealthcheck() {
	if err := healthcheck(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
