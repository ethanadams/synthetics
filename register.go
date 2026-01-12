package synthetics

// This file imports the xk6-storj extension so that xk6 can discover and register it.
// When xk6 builds with --with github.com/ethanadams/synthetics, it will import this package,
// which triggers the init() function in the xk6-storj subpackage.

import (
	_ "github.com/ethanadams/synthetics/cmd/xk6-storj" // Import for side effects (init registration)
)
