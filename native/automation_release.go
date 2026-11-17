//go:build !e2e

package main

func automationName(name string, _ bool) (string, error) { return name, nil }
func automationShell(_ *shell)                           {}
