// Package embeddedscripts embeds the Ruby DSL scripts into the Go binary at compile time.
//
// This file lives at the module root because Go's //go:embed directive can only
// reference files in the same directory or subdirectories. The scripts/ directory
// is at the module root, so the embed directive must be here.
//
// Import this package as:
//
//	import embeddedscripts "terraform-provider-conveyor-belt"
package embeddedscripts

import "embed"

//go:embed scripts/*
//go:embed scripts/lib/*
var Scripts embed.FS
