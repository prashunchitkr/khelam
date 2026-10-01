// Package version reports the build version, set at link time:
//
//	go build -ldflags "-X github.com/prashunchitkr/khelam/internal/version.Version=v1.2.3"
package version

import "runtime/debug"

// Version is the release version, or "dev" for local builds.
var Version = "dev"

// String returns the version, falling back to the VCS revision embedded
// by the Go toolchain for untagged builds.
func String() string {
	if Version != "dev" {
		return Version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}

	var revision, modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			if setting.Value == "true" {
				modified = "-dirty"
			}
		}
	}

	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision == "" {
		return Version
	}
	return Version + "-" + revision + modified
}
