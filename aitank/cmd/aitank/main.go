// Command aitank shows how much usage is left on each AI plan and which
// account to use next.
package main

import (
	"os"

	"github.com/himanshumehta/flexrouter/aitank/internal/cli"
	_ "github.com/himanshumehta/flexrouter/aitank/internal/providers/all"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
