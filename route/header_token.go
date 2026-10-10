package route

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS-Common/utils/jwt"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/labstack/echo/v4"
)

// refreshIssuer is the issuer of the long-lived token that only gets a new access token.
const refreshIssuer = "refresh"

// headerTokenOnly is the middleware of a route that stops every container on the box. The group
// takes the token from the Authorization header, or from ?token= when there is no header, which
// a log, a history or a Referer can leak; and it takes the refresh token, which lives for a
// week, as an access token. This route wants neither: the access token, in the header.
//
// It runs after the group's JWT check, which puts the token's claims in the context. A request
// that has none is one of the box's own services (skipJWT), which the group let in on the secret
// of this boot: it carries the header "Authorization: Internal <secret>".
func headerTokenOnly(next echo.HandlerFunc) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		if ctx.Request().Header.Get(echo.HeaderAuthorization) == "" {
			return unauthorized(ctx, "the token goes in the Authorization header")
		}
		switch claims := ctx.Get("user").(type) {
		case nil:
			// no claims: an internal request
		case *jwt.Claims:
			if claims == nil || claims.Issuer == refreshIssuer {
				return unauthorized(ctx, "a refresh token cannot do this")
			}
		default:
			return unauthorized(ctx, "the token is not understood")
		}
		return next(ctx)
	}
}

func unauthorized(ctx echo.Context, message string) error {
	return ctx.JSON(http.StatusUnauthorized, model.Result{Success: http.StatusUnauthorized, Message: message})
}
