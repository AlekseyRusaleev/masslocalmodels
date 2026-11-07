//go:build !windows

package main

import "fmt"

// Графический режим есть только на Windows: на сервере узел работает службой.
func runGUI() int {
	fmt.Println("на Linux окна нет: узел ставится и управляется командами")
	fmt.Println("  sudo compute-node setup --code <код>")
	fmt.Println("  sudo compute-node start")
	fmt.Println("  sudo compute-node stop")
	fmt.Println("  sudo compute-node pause")
	fmt.Println("  sudo compute-node resume")
	fmt.Println("  compute-node status")
	fmt.Println("  sudo compute-node update")
	return 2
}
