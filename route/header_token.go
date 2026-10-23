package route

import (
	"crypto/ecdsa"
	"net/http"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/jwt"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/labstack/echo/v4"
)

// accessIssuer is the issuer of the access token the user service mints (CasaOS-Common jwt.GetAccessToken). The refresh
// token ("refresh"), which lives for a week, only gets a new access token; no other issuer is expected.
const accessIssuer = "casaos"

// accessClaims are the claims of a valid access token, and of nothing else signed with the same key.
func accessClaims(token string) (*jwt.Claims, error) {
	valid, claims, err := jwt.Validate(token, func() (*ecdsa.PublicKey, error) { return external.GetPublicKey(config.CommonInfo.RuntimePath) })
	if err != nil || !valid || claims.Issuer != accessIssuer {
		return nil, echo.ErrUnauthorized
	}
	return claims, nil
}

// lookupToken takes the token from the Authorization header, or from ?token= on a GET: a link, an image, a download or a
// websocket (whose upgrade is a GET) cannot set a header. A call that changes something can, and the address leaks into
// logs, histories and Referers.
func lookupToken(ctx echo.Context) ([]string, error) {
	if authorization := ctx.Request().Header.Get(echo.HeaderAuthorization); authorization != "" {
		return []string{authorization}, nil
	}
	if ctx.Request().Method == http.MethodGet {
		return []string{ctx.QueryParam("token")}, nil
	}
	return nil, nil
}

// headerTokenOnly is the middleware of a route that stops every container on the box. The group
// takes ?token= on a GET, which a log, a history or a Referer can leak, and only the access token
// (lookupToken, accessClaims). This route wants the access token in the header, whatever the
// group would take.
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
			if claims == nil || claims.Issuer != accessIssuer {
				return unauthorized(ctx, "only the access token can do this")
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
