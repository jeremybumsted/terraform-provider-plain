package plain

import tfpath "github.com/hashicorp/terraform-plugin-framework/path"

// path is a small helper so provider-level diagnostics read cleanly.
func path(attr string) tfpath.Path { return tfpath.Root(attr) }
