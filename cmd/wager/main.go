// Command wager sobe o processo HTTP da liquidação.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/junglegaming/backend-challenge-go/internal/platform/app"
)

func main() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	if err := app.LoadAndRun(os.Getenv, sigs, app.Boot); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
