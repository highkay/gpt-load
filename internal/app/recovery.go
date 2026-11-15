package app

import (
	"net/http"

	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// recoveryMiddleware converts handler panics into the standard internal-error
// response without logging request data, credentials, or the raw panic value.
func recoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered == nil {
				return
			} else if recovered == http.ErrAbortHandler {
				// Let net/http close the connection instead of finishing a
				// partially written response as a normal success.
				panic(recovered)
			}

			logrus.Error("Recovered from HTTP handler panic")
			if !c.Writer.Written() {
				response.Error(c, app_errors.ErrInternalServer)
			}
			c.Abort()
		}()

		c.Next()
	}
}
