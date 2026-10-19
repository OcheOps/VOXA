// Package migrations embeds the ordered SQL schema migrations for the Voxa
// server. Files are named NNNN_description.sql and are applied in version order.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
