package main

import (
	"os"

	"aliang.one/nursorgate/cmd"
)

func main() {
	handled, err := cmd.MaybeRunAsWindowsService()
	if handled {
		if err != nil {
			os.Exit(1)
		}
		return
	}

	if cmd.MaybeRunWindowsCompanionFromArgs() {
		return
	}

	cmd.Execute()
}
