//go:build !windows

package main

// attachConsole is a no-op: other platforms always have usable std handles.
func attachConsole() bool { return true }

func logToFile(stateDir string) {}

func hasConsoleWindow() bool { return true }
