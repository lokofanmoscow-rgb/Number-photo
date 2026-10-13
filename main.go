// NumberPhoto places part numbers at the free ends of black leader lines drawn
// on a photo.
//
// Without arguments (or with an image path) it starts the graphical program
// (Windows). With -cli it processes files from the command line:
//
//	NumberPhoto.exe -cli -start 69 input.jpg output.jpg
package main

import (
	"os"
)

const appTitle = "Нумерация фото"
const appVersion = "1.0"

func main() {
	if wantCLI(os.Args[1:]) {
		os.Exit(runCLI(os.Args[1:]))
	}
	runGUI(os.Args[1:])
}

func wantCLI(args []string) bool {
	for _, a := range args {
		if a == "-cli" || a == "--cli" || a == "-h" || a == "-help" || a == "--help" {
			return true
		}
	}
	return false
}
