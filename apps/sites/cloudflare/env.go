package cloudflare

import "os"

// getenv is the ONE door this package reads the environment through, so a test
// can steer it and a reader can find every environment dependency by grepping a
// single name.
var getenv = os.Getenv
