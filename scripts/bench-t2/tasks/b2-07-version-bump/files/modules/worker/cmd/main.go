package main

import (
	"fmt"

	"example.com/harbor/modules/worker"
)

func main() {
	fmt.Printf("worker %s (min %s)", worker.Version, worker.MinCompatible)
	fmt.Println()
}
