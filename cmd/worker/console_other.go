//go:build !windows

package main

// enableConsole вне Windows ничего не делает: там программа всегда
// консольная, а графический режим отсутствует.
func enableConsole() {}
