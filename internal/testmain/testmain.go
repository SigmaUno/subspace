package testmain

import (
	"fmt"
	"os"
	"testing"

	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func Run(m *testing.M) {
	if err := sqlite.EnableTemplate(); err != nil {
		fmt.Fprintln(os.Stderr, "sqlite template:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
