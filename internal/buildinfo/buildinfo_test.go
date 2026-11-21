package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	vcs := &debug.BuildInfo{
		GoVersion: "go1.27.3",
		Main:      debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "vcscommit"},
			{Key: "vcs.time", Value: "2026-11-01T10:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	tests := []struct {
		name                     string
		bi                       *debug.BuildInfo
		ldVersion, ldCommit, ldT string
		want                     Info
	}{
		{"nothing", nil, "", "", "", Info{Version: "dev"}},
		{
			"vcs fallback", vcs, "", "", "",
			Info{Version: "dev", Commit: "vcscommit", CommitTime: "2026-11-01T10:00:00Z", Modified: true, GoVersion: "go1.27.3"},
		},
		{
			"ldflags win", vcs, "v1.2.3", "ldcommit", "2026-11-02T00:00:00Z",
			Info{
				Version: "v1.2.3", Commit: "ldcommit", CommitTime: "2026-11-01T10:00:00Z",
				BuildTime: "2026-11-02T00:00:00Z", Modified: true, GoVersion: "go1.27.3",
			},
		},
		{
			"module version", &debug.BuildInfo{Main: debug.Module{Version: "v0.4.0"}}, "", "", "",
			Info{Version: "v0.4.0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := resolve(tt.bi, tt.ldVersion, tt.ldCommit, tt.ldT); got != tt.want {
				t.Errorf("resolve() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
