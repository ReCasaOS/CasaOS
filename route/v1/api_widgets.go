package v1

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/apiwidgets"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

const (
	// a change: a recipe of 16 KiB at most and its secrets
	maxWidgetBody = 32 << 10
	// a preview carries an answer of 256 KiB at most beside the recipe
	maxWidgetPreviewBody = 320 << 10
)

var widgetIDPattern = regexp.MustCompile(`^[a-z0-9]{8}$`)

// widgetBody reads the JSON body of a request into into, limit bytes at most. A
// body over the limit is refused whole. When it answers false, the answer is
// written. The body holds secrets: nothing of it, and nothing of what the decoder
// says of it, is answered or logged.
func widgetBody(ctx echo.Context, limit int64, into any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(ctx.Response(), ctx.Request().Body, limit))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		_ = ctx.JSON(http.StatusRequestEntityTooLarge, model.Result{Success: http.StatusRequestEntityTooLarge, Message: "the body is too large"})
	case err != nil || json.Unmarshal(raw, into) != nil:
		_ = ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: "the body is not JSON"})
	default:
		return true
	}
	return false
}

// widgetAnswer answers data, or what err is and nothing of data: the hub's errors
// are written to be shown, and any other may name a file, so its text stays here.
func widgetAnswer(ctx echo.Context, data any, err error) error {
	switch {
	case err == nil:
		return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
	case errors.Is(err, apiwidgets.ErrNotFound):
		return ctx.JSON(http.StatusNotFound, model.Result{Success: http.StatusNotFound, Message: err.Error()})
	case errors.Is(err, apiwidgets.ErrSecretAgain):
		// the dashboard empties every input and asks for them again
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error(), Data: map[string]bool{"secrets_again": true}})
	case errors.Is(err, apiwidgets.ErrInvalid), errors.Is(err, apiwidgets.ErrRead):
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: "the widgets could not be read or written"})
}

// widgetID is the id in the path, or "" when it is not one: no widget has it.
func widgetID(ctx echo.Context) string {
	if id := ctx.Param("id"); widgetIDPattern.MatchString(id) {
		return id
	}
	return ""
}

// GetAPIWidgets answers the API widgets of the dashboard: for each, its address,
// its recipe and whether each input is filled. No secret leaves.
//
// @Summary API widgets: the list
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/widgets [get]
func GetAPIWidgets(ctx echo.Context) error {
	return widgetAnswer(ctx, map[string][]apiwidgets.View{"widgets": apiwidgets.Default.List()}, nil)
}

// PostAPIWidget creates a widget from {base, insecure_tls, recipe, secrets}: at
// most twelve, every input filled, else a 400 that says what is refused.
//
// @Summary API widgets: create one
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/widgets [post]
func PostAPIWidget(ctx echo.Context) error {
	var change apiwidgets.Change
	if !widgetBody(ctx, maxWidgetBody, &change) {
		return nil
	}
	view, err := apiwidgets.Default.Create(change)
	return widgetAnswer(ctx, view, err)
}

// PutAPIWidget replaces a widget. An input left out keeps its secret as long as
// the request stays the same; when the address or the request changes, the 400
// carries data {"secrets_again": true} and every input must come again.
//
// @Summary API widgets: change one
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param id path string true "the id of the widget"
// @Success 200 {object} model.Result
// @Router /sys/widgets/{id} [put]
func PutAPIWidget(ctx echo.Context) error {
	id := widgetID(ctx)
	if id == "" {
		return widgetAnswer(ctx, nil, apiwidgets.ErrNotFound)
	}
	var change apiwidgets.Change
	if !widgetBody(ctx, maxWidgetBody, &change) {
		return nil
	}
	view, err := apiwidgets.Default.Update(id, change)
	return widgetAnswer(ctx, view, err)
}

// DeleteAPIWidget removes a widget, its secrets and its card.
//
// @Summary API widgets: remove one
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param id path string true "the id of the widget"
// @Success 200 {object} model.Result
// @Router /sys/widgets/{id} [delete]
func DeleteAPIWidget(ctx echo.Context) error {
	id := widgetID(ctx)
	if id == "" {
		return widgetAnswer(ctx, nil, apiwidgets.ErrNotFound)
	}
	return widgetAnswer(ctx, nil, apiwidgets.Default.Delete(id))
}

// GetAPIWidgetCard answers the card of a widget, read by the box and kept the
// time its recipe says. A read that fails is still a card, answered 200: its
// error says why, and the dashboard shows the widget as unavailable.
//
// @Summary API widgets: the card of one
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param id path string true "the id of the widget"
// @Success 200 {object} model.Result
// @Router /sys/widgets/{id}/card [get]
func GetAPIWidgetCard(ctx echo.Context) error {
	id := widgetID(ctx)
	if id == "" {
		return widgetAnswer(ctx, nil, apiwidgets.ErrNotFound)
	}
	card, err := apiwidgets.Default.Card(ctx.Request().Context(), id)
	return widgetAnswer(ctx, card, err)
}

// PostAPIWidgetTest reads the address of a change, which may still be a draft,
// and answers {response, card}: what the address gave and the card the recipe
// makes of it. With the "id" of a widget, the inputs left out take its secrets, by
// the rule of a change. Nothing is written. A read that fails is a 400 with its
// reason.
//
// @Summary API widgets: try an address and a recipe
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/widgets/test [post]
func PostAPIWidgetTest(ctx echo.Context) error {
	var body struct {
		apiwidgets.Change
		ID string `json:"id"`
	}
	if !widgetBody(ctx, maxWidgetBody, &body) {
		return nil
	}
	result, err := apiwidgets.Default.Test(ctx.Request().Context(), body.ID, body.Change)
	return widgetAnswer(ctx, result, err)
}

// PostAPIWidgetPreview answers the card a recipe, which may still be a draft,
// makes of {recipe, response}, an answer the dashboard already has from a test.
// The box sends nothing.
//
// @Summary API widgets: the card of a recipe on an answer already read
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/widgets/preview [post]
func PostAPIWidgetPreview(ctx echo.Context) error {
	var body struct {
		Recipe   json.RawMessage `json:"recipe"`
		Response json.RawMessage `json:"response"`
	}
	if !widgetBody(ctx, maxWidgetPreviewBody, &body) {
		return nil
	}
	card, err := apiwidgets.Default.Preview(body.Recipe, body.Response)
	return widgetAnswer(ctx, card, err)
}
