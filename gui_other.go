//go:build !windows

package main

import (
	"fmt"
	"os"
)

func attachConsole() {}

func runGUI(args []string) {
	if len(args) > 0 {
		os.Exit(runCLI(append([]string{"-cli"}, args...)))
	}
	fmt.Println("Графический интерфейс доступен только в Windows. Используйте: NumberPhoto -cli вход.jpg [выход.jpg]")
	os.Exit(2)
}
