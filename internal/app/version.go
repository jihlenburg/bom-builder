// Package app contains build identity shared by command surfaces.
package app

// Version is the semantic version reported by the executable. Release builds
// may override it with -ldflags "-X github.com/jihlenburg/bom-builder/internal/app.Version=...".
var Version = "3.0.0-dev"
