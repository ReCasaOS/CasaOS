package v1

import (
	"bytes"
	"net/http"
	"os/exec"
	"strconv"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/common_err"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	sshHelper "github.com/ReCasaOS/CasaOS-Common/utils/ssh"
	"github.com/ReCasaOS/CasaOS/pkg/utils"
	"github.com/labstack/echo/v4"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	modelCommon "github.com/ReCasaOS/CasaOS-Common/model"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:   1024,
	WriteBufferSize:  1024,
	CheckOrigin:      func(r *http.Request) bool { return true },
	HandshakeTimeout: time.Duration(time.Second * 5),
}

func PostSshLogin(ctx echo.Context) error {
	j := make(map[string]string)
	ctx.Bind(&j)
	userName := j["username"]
	password := j["password"]
	port := j["port"]
	if userName == "" || password == "" || port == "" {
		return ctx.JSON(common_err.CLIENT_ERROR, modelCommon.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS), Data: "Username or password or port is empty"})
	}
	_, err := sshHelper.NewSshClient(userName, password, port)
	if err != nil {
		logger.Error("connect ssh error", zap.Any("error", err))
		return ctx.JSON(common_err.CLIENT_ERROR, modelCommon.Result{Success: common_err.CLIENT_ERROR, Message: common_err.GetMsg(common_err.CLIENT_ERROR), Data: "Please check if the username and port are correct, and make sure that ssh server is installed."})
	}
	return ctx.JSON(common_err.SUCCESS, modelCommon.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// closeWith ends the socket with a close frame that says why, which the browser can show instead of a bare 1006.
func closeWith(wsConn *websocket.Conn, reason string) {
	_ = wsConn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason), time.Now().Add(time.Second))
	wsConn.Close()
}

func WsSsh(ctx echo.Context) error {
	_, e := exec.LookPath("ssh")
	if e != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, modelCommon.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: "ssh server not found"})
	}

	userName := ctx.QueryParam("username")
	password := ctx.QueryParam("password")
	port := ctx.QueryParam("port")
	wsConn, err := upgrader.Upgrade(ctx.Response().Writer, ctx.Request(), nil)
	if err != nil {
		return nil // the upgrader has answered the request
	}
	logBuff := new(bytes.Buffer)

	quitChan := make(chan bool, 3)
	cols, _ := strconv.Atoi(utils.DefaultQuery(ctx, "cols", "200"))
	rows, _ := strconv.Atoi(utils.DefaultQuery(ctx, "rows", "32"))
	// One attempt: the browser asks again if it wants to. Retrying at once looped forever, after the browser had gone too.
	if userName == "" || password == "" || port == "" {
		wsConn.WriteMessage(websocket.TextMessage, []byte("username or password or port is empty"))
		closeWith(wsConn, "username, password or port is empty")
		return nil
	}
	client, err := sshHelper.NewSshClient(userName, password, port)
	if err != nil {
		wsConn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		wsConn.WriteMessage(websocket.TextMessage, []byte("\r\n\x1b[0m"))
		closeWith(wsConn, "ssh login failed")
		return nil
	}
	defer client.Close()

	ssConn, err := sshHelper.NewSshConn(cols, rows, client)
	if err != nil {
		wsConn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		closeWith(wsConn, "ssh session failed")
		return nil
	}
	defer ssConn.Close()

	go ssConn.ReceiveWsMsg(wsConn, logBuff, quitChan)
	go ssConn.SendComboOutput(wsConn, quitChan)
	go ssConn.SessionWait(quitChan)

	<-quitChan
	return nil
}
