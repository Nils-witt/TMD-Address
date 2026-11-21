// Package buildinfo describes the running binary: the values set at link time
// (see Dockerfile and .goreleaser.yml), completed by the VCS data the Go
// toolchain embeds when building inside a git checkout. It matches the api's
// buildinfo, so GET /api/version answers the same JSON on both services.
package buildinfo

import (
	"runtime/debug"
	"strconv"
)

// Info describes the running build.
type Info struct {
	// Version is the release tag (e.g. v1.2.3), or "dev" when unknown.
	Version string `json:"version"`
	// Commit is the full git commit hash, empty when unknown.
	Commit string `json:"commit"`
	// CommitTime is the commit timestamp (RFC 3339), empty when unknown.
	CommitTime string `json:"commit_time"`
	// BuildTime is the build timestamp (RFC 3339), empty when unknown.
	BuildTime string `json:"build_time"`
	// Modified reports whether the working tree had uncommitted changes.
	Modified bool `json:"modified"`
	// GoVersion is the Go toolchain that built the binary.
	GoVersion string `json:"go_version"`
}

// Get returns the build information of the running binary. The arguments are
// the values set at link time; empty ones fall back to the embedded VCS data.
func Get(version, commit, buildTime string) Info {
	bi, _ := debug.ReadBuildInfo()

	return resolve(bi, version, commit, buildTime)
}

func resolve(bi *debug.BuildInfo, ldVersion, ldCommit, ldBuildTime string) Info {
	info := Info{Version: ldVersion, Commit: ldCommit, BuildTime: ldBuildTime}

	if bi != nil {
		info.GoVersion = bi.GoVersion

		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				info.CommitTime = s.Value
			case "vcs.modified":
				info.Modified, _ = strconv.ParseBool(s.Value)
			}
		}

		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
	}

	if info.Version == "" {
		info.Version = "dev"
	}

	return info
}
