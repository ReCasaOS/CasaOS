package route

import (
	"strings"

	echo_middleware "github.com/labstack/echo/v4/middleware"
)

// accessLogFormat is echo's default access log with the path in place of the
// request URI: websocket routes take the user's token from the query string
// (a browser cannot set a header on a websocket), and it must not reach the
// journal.
var accessLogFormat = strings.Replace(echo_middleware.DefaultLoggerConfig.Format, `"uri":"${uri}"`, `"path":"${path}"`, 1)
