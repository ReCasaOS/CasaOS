package v1

import (
	"errors"
	"net/http"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/pkg/weather"
	"github.com/labstack/echo/v4"
)

// GetWeather answers the dashboard's weather: the switch, the place, the units and
// the forecast (CasaOS#188).
//
// @Summary dashboard weather: settings and forecast
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/weather [get]
func GetWeather(ctx echo.Context) error {
	return weatherAnswer(ctx, weather.Default.Status(), nil)
}

// PutWeather sets {enabled, place, units}; on needs a place, else a 400.
//
// @Summary dashboard weather: set the switch, the place and the units
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/weather [put]
func PutWeather(ctx echo.Context) error {
	var change weather.Config
	if err := ctx.Bind(&change); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	status, err := weather.Default.Update(change)
	return weatherAnswer(ctx, status, err)
}

// GetWeatherPlaces finds places by ?name= (two letters at least), named in ?language=.
//
// @Summary dashboard weather: find a place
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/weather/places [get]
func GetWeatherPlaces(ctx echo.Context) error {
	places, err := weather.Default.Search(ctx.QueryParam("name"), ctx.QueryParam("language"))
	return weatherAnswer(ctx, places, err)
}

func weatherAnswer(ctx echo.Context, data any, err error) error {
	if errors.Is(err, weather.ErrInvalid) {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	if err != nil {
		return ctx.JSON(http.StatusBadGateway, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    data,
	})
}
