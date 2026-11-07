//go:build !windows

package main

import "fmt"

// Графический режим есть только на Windows: на сервере узел работает службой.
func runGUI() int {
	fmt.Println("графический режим доступен только на Windows")
	fmt.Println("используйте: compute-node join --invite <код>")
	return 2
}
