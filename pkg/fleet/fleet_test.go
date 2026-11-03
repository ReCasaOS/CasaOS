package fleet

import (
	"os"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

func TestMain(m *testing.M) {
	// The package logs through CasaOS-Common's logger, nil until initialised.
	logger.LogInitConsoleOnly()
	os.Exit(m.Run())
}
