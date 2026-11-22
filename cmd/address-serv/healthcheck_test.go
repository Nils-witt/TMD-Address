package main

import "testing"

func TestLoopbackAddr(t *testing.T) {
	t.Parallel()

	for listen, want := range map[string]string{
		":8080":          "127.0.0.1:8080",
		"0.0.0.0:8080":   "127.0.0.1:8080",
		"[::]:8080":      "127.0.0.1:8080",
		"127.0.0.1:9000": "127.0.0.1:9000",
		"[::1]:9000":     "[::1]:9000",
		"example:80":     "example:80",
	} {
		if got := loopbackAddr(listen); got != want {
			t.Errorf("loopbackAddr(%q) = %q, want %q", listen, got, want)
		}
	}
}
