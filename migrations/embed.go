// Package migrations embeds the SQL migration files into the publisher
// binary so deployments do not require shipping the migrations/ directory
// alongside the executable.
//
// The fs.FS exposed by FS is consumed by internal/dbinit at startup via
// golang-migrate's iofs source driver.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
