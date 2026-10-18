// Package boot runs the module assistant (`casaos module ...`) before anything else of the core is initialised.
//
// Go initialises the packages of a program in the order of their import paths, among those whose imports are done, and this path is the first of the core's own: when the packages that the
// assistant needs (pkg/modulecmd and what it imports, none of which does anything when it is initialised) are done, this one is next, and the assistant runs and ends the process
// here, before route/v1 has started its goroutine and its timer, before route has read its API description and before service, alerts and telemetry have been set up. What is
// initialised first, then, is what the assistant itself imports and the standard library; the test of this package builds the core and reads the trace of its initialisation to say so.
//
// main.go imports this package for this and nothing else; its own init keeps the dispatch as well, for a build in which this package is not imported, so that `casaos module` never
// falls through to starting the core.
package boot

import (
	"os"

	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
)

func init() {
	if rest, ok := modulecmd.Dispatch(os.Args); ok {
		os.Exit(modulecmd.Run(rest, modulecmd.Production()))
	}
}
