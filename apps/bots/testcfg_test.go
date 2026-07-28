package bots

import (
	"time"

	"github.com/zap-proto/fiber/v3"
)

// testCfg replaces fiber's Test() default of Timeout: 1s (fiber/v3@v3.2.1
// app.go:1202, FailOnTimeout: true). That default is a WALL-CLOCK deadline on an
// in-process request: under load — a full suite, encrypted SQLite, a busy box — a
// correct handler blows it and the test reports "i/o timeout". A guard that fails
// for reasons unrelated to what it guards teaches nothing, and a tenant-isolation
// guard that is a coin flip is worse than none. The generous bound still fails a
// genuine hang.
var testCfg = fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}
