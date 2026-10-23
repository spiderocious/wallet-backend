// Command migrate applies pending migrations. Run it as a release step.
package main

import (
	"fmt"
	"os"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/migrations"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}
	if err := migrations.Up(dsn); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("migrations applied")
}
