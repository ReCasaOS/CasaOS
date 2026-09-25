package v1

import (
	"os"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

func TestMain(m *testing.M) {
	// The handlers log through CasaOS-Common's logger, nil until initialised:
	// an alerts test whose channel fails logs its failure.
	logger.LogInitConsoleOnly()
	os.Exit(m.Run())
}
