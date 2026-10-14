package protocol

import "runtime/debug"

const (
	implName       = "humbug"
	libOpenAPI     = "github.com/pb33f/libopenapi"
	libOpenAPIVal  = "github.com/pb33f/libopenapi-validator"
	unknownVersion = "unknown"
)

// implementation describes this build for `initialize`: the release and the exact versions of the
// libraries whose behavior decides which findings are reported (see docs/versioning.md). It is for
// support and bug reports; a client must not branch on it.
func implementation() map[string]any {
	version := unknownVersion
	libs := map[string]string{"libopenapi": unknownVersion, "libopenapi_validator": unknownVersion}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" {
			version = v // "(devel)" for a build from a working tree
		}
		for _, dep := range bi.Deps {
			v := dep.Version
			if dep.Replace != nil {
				v = dep.Replace.Version
			}
			switch dep.Path {
			case libOpenAPI:
				libs["libopenapi"] = v
			case libOpenAPIVal:
				libs["libopenapi_validator"] = v
			}
		}
	}
	return map[string]any{"name": implName, "version": version, "libraries": libs}
}
