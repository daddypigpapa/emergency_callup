// Package web embeds the entire frontend (SPEC §3.3) into the server
// binary via go:embed, so deployment is a single file plus /data.
package web

import "embed"

//go:embed f a shared vendor sw.js
var Files embed.FS
