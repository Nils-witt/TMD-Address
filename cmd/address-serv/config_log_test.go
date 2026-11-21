package main

import (
	"strings"
	"testing"
)

// testPassword is a made-up secret the masked output must not contain.
const testPassword = "verysecretpw42"

func TestMaskSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, want string
	}{
		{name: "long keeps last five", in: "supersecretpassword", want: "*****sword"},
		{name: "eleven chars keeps last five", in: "abcdefghijk", want: "*****ghijk"},
		{name: "ten chars hidden", in: "abcdefghij", want: "*****"},
		{name: "short hidden", in: "abc", want: "*****"},
		{name: "multibyte counted as runes", in: "äöüäöüäöüäöü", want: "*****öüäöü"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := maskSecret(tt.in); got != tt.want {
				t.Errorf("maskSecret(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMaskDSN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in, want, secret string
	}{
		{ //nolint:gosec // G101: a made-up test DSN, not a credential.
			name:   "url password",
			in:     "postgres://app:verysecretpw42@db:5432/app?sslmode=disable",
			want:   "postgres://app:%2A%2A%2A%2A%2Atpw42@db:5432/app?sslmode=disable",
			secret: testPassword,
		},
		{
			name:   "query password",
			in:     "postgres://db/app?password=verysecretpw42",
			want:   "postgres://db/app?password=%2A%2A%2A%2A%2Atpw42",
			secret: testPassword,
		},
		{name: "no password", in: "postgres://app@db/app", want: "postgres://app@db/app"},
		{
			name:   "key value dsn masked whole",
			in:     "host=db user=app password=verysecretpw42",
			want:   "*****tpw42",
			secret: testPassword,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := maskDSN(tt.in)
			if tt.secret != "" && strings.Contains(got, tt.secret) {
				t.Fatalf("maskDSN(%q) = %q leaks the password", tt.in, got)
			}

			if got != tt.want {
				t.Errorf("maskDSN(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatConfigEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry configEntry
		value string
		want  string
	}{
		{name: "set", entry: configEntry{key: "LISTEN_ADDR", def: ":8080"}, value: ":9090", want: `LISTEN_ADDR=":9090"`},
		{name: "default", entry: configEntry{key: "LISTEN_ADDR", def: ":8080"}, want: "LISTEN_ADDR unset (default: :8080)"},
		{name: "unset without default", entry: configEntry{key: "OIDC_AUDIENCE"}, want: "OIDC_AUDIENCE unset"},
		{
			name:  "secret",
			entry: configEntry{key: "POSTGRES_PASSWORD", mask: maskSecret},
			value: testPassword,
			want:  `POSTGRES_PASSWORD="*****tpw42"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := formatConfigEntry(tt.entry, tt.value); got != tt.want {
				t.Errorf("formatConfigEntry() = %q, want %q", got, tt.want)
			}
		})
	}
}
