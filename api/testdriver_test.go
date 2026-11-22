package api

import "os"

// testDriverEnv selects the database driver for tests that can run on more than
// SQLite. With no environment it returns ("sqlite", ""), keeping the default
// `go test ./...` hermetic; set TC_TEST_DRIVER and TC_TEST_DSN to point the same
// assertions at a real MySQL or PostgreSQL server.
func testDriverEnv() (driver, dsn string) {
	driver = os.Getenv("TC_TEST_DRIVER")
	if driver == "" {
		return "sqlite", ""
	}
	return driver, os.Getenv("TC_TEST_DSN")
}
