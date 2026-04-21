// Package tests contains black-box, cross-module integration-style tests
// that exercise the public surfaces of internal/app, internal/notifier and
// internal/xapi. Unit tests colocated with sources (e.g. internal/app/service_test.go)
// remain the first line of coverage; this package focuses on end-to-end
// behaviour, edge cases and interaction between packages.
//
// Run with:
//
//	go test ./tests/...
package tests
