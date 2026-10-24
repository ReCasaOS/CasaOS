/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2022-07-26 11:08:48
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-17 18:25:42
 * @FilePath: /CasaOS/route/v1/samba.go
 * @Description:
 * @Website: https://www.casaos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package v1

import (
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/utils/systemctl"
	"github.com/labstack/echo/v4"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/samba"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

// service

func GetSambaStatus(ctx echo.Context) error {
	if status, err := systemctl.IsServiceRunning("smbd"); err != nil || !status {
		return ctx.JSON(http.StatusInternalServerError, model.Result{
			Success: common_err.SERVICE_NOT_RUNNING,
			Message: common_err.GetMsg(common_err.SERVICE_NOT_RUNNING),
		})
	}

	// external: smb.conf is the host's, CasaOS never initialises it
	needInit := !service.SambaMainConfigExternal()
	if needInit && file.Exists("/etc/samba/smb.conf") {
		str := file.ReadLine(1, "/etc/samba/smb.conf")
		if strings.Contains(str, "# Copyright (c) 2021-2022 CasaOS Inc. All rights reserved.") {
			needInit = false
		}
	}
	data := make(map[string]string, 1)
	data["need_init"] = fmt.Sprintf("%v", needInit)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}

func GetSambaSharesList(ctx echo.Context) error {
	shares := service.MyService.Shares().GetSharesList()
	shareList := []model.Shares{}
	for _, v := range shares {
		shareList = append(shareList, model.Shares{
			Anonymous:   v.Anonymous,
			Path:        v.Path,
			ID:          v.ID,
			Username:    v.Username,
			TimeMachine: v.TimeMachine,
		})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: shareList})
}

// restrictShareToUser hands the shared directory to the account allowed to mount
// it.
//
// Without this the directory stays root-owned, while the Samba session runs as
// that account: the share would authenticate correctly and then refuse every
// write, which reads as a broken feature rather than a permissions problem.
//
// path must already have been through service.ValidateSharePath.
func restrictShareToUser(path, username string) error {
	account, err := user.Lookup(username)
	if err != nil {
		return err
	}

	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}

	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}

	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}

	return os.Chmod(path, 0o770)
}

// releaseShareFromUser gives a directory back to root when its share goes away.
//
// Leaving it owned by a share account would keep that account in control of the
// folder long after the share, and the account itself, are gone.
func releaseShareFromUser(path string) error {
	if _, err := service.ValidateSharePath(path); err != nil {
		return err
	}

	if err := os.Chown(path, 0, 0); err != nil {
		return err
	}

	return os.Chmod(path, 0o755)
}

func PostSambaSharesCreate(ctx echo.Context) error {
	shares := []model.Shares{}
	if err := ctx.Bind(&shares); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	// Sections in smb.casa.conf are named after the directory. smbd merges a
	// repeated section into the first one, so a second share with the same
	// basename silently disappears. The database checks below cannot see the
	// other entries of this same request, hence the local set.
	requested := map[string]bool{}
	resolved := make([]string, len(shares))

	for i, v := range shares {
		if v.Path == "" {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INSUFFICIENT_PERMISSIONS, Message: common_err.GetMsg(common_err.INSUFFICIENT_PERMISSIONS)})
		}

		// Sharing a folder changes its ownership and permissions as root, so the
		// path is confined before anything touches it.
		path, err := service.ValidateSharePath(v.Path)
		if err != nil {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.DIR_NOT_EXISTS, Message: err.Error()})
		}

		resolved[i] = path
		name := filepath.Base(path)

		if requested[name] {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.SHARE_NAME_ALREADY_EXISTS, Message: common_err.GetMsg(common_err.SHARE_NAME_ALREADY_EXISTS)})
		}

		requested[name] = true

		if len(service.MyService.Shares().GetSharesByPath(path)) > 0 {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.SHARE_ALREADY_EXISTS, Message: common_err.GetMsg(common_err.SHARE_ALREADY_EXISTS)})
		}

		// This used to query the path column with a bare basename, so it never
		// matched and the duplicate-name error was unreachable.
		if len(service.MyService.Shares().GetSharesByName(name)) > 0 {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.SHARE_NAME_ALREADY_EXISTS, Message: common_err.GetMsg(common_err.SHARE_NAME_ALREADY_EXISTS)})
		}

		// A share is protected when it names an account, not when a boolean says
		// so. Clients that predate this field omit it and keep getting the guest
		// share they have always got.
		if v.Username != "" {
			if err := service.ValidateSambaUsername(v.Username); err != nil {
				return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
			}

			if _, err := user.Lookup(v.Username); err != nil {
				return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.USER_NOT_EXIST, Message: common_err.GetMsg(common_err.USER_NOT_EXIST)})
			}
		}

		// Refuse up front rather than handing back a share that mounts and then
		// refuses every connection: testparm cannot see a missing VFS module.
		if v.TimeMachine {
			if err := service.ValidateTimeMachineSupport(); err != nil {
				return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
			}
		}
	}

	for i, v := range shares {
		path := resolved[i]

		shareDBModel := model2.SharesDBModel{
			Anonymous:   v.Username == "",
			Username:    v.Username,
			Path:        path,
			Name:        filepath.Base(path),
			TimeMachine: v.TimeMachine,
		}

		if v.Username == "" {
			// Unchanged from before authenticated shares existed: a guest share has
			// no owner to speak of, so the directory stays world-writable.
			os.Chmod(path, 0o777)
		} else if err := restrictShareToUser(path, v.Username); err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}

		if err := service.MyService.Shares().CreateShare(shareDBModel); err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: shares})
}

// PutSambaShare moves an existing share between guest access and a named
// account, in either direction, and turns its Time Machine flag on or off. An
// empty username makes it a guest share again.
//
// The body replaces both fields, so a client that omits time_machine clears it.
//
// Only the shared directory itself is re-owned, never its contents. A media
// share can hold millions of files and walking them inside an HTTP request is
// not something a NAS should be asked to do. It is not needed either: files
// created under a guest share are world-readable, and files created under a
// protected share stay reachable by root, which is who a guest session maps to.
func PutSambaShare(ctx echo.Context) error {
	id := ctx.Param("id")

	share, found := service.MyService.Shares().GetShareByID(id)
	if !found {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: "no such share"})
	}

	request := model.Shares{}
	if err := ctx.Bind(&request); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	if request.Username == share.Username && request.TimeMachine == share.TimeMachine {
		return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: share})
	}

	path, err := service.ValidateSharePath(share.Path)
	if err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.DIR_NOT_EXISTS, Message: err.Error()})
	}

	if request.Username != "" {
		if err := service.ValidateSambaUsername(request.Username); err != nil {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
		}

		if _, err := user.Lookup(request.Username); err != nil {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.USER_NOT_EXIST, Message: common_err.GetMsg(common_err.USER_NOT_EXIST)})
		}
	}

	if request.TimeMachine {
		if err := service.ValidateTimeMachineSupport(); err != nil {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
		}
	}

	// Ownership moves first. If Samba then refuses the configuration the share
	// keeps working as it did, and the directory is put back below. A request
	// that only flips the Time Machine flag must leave the directory alone:
	// releaseShareFromUser would drop a guest folder from 0777 to 0755.
	usernameChanged := request.Username != share.Username
	if usernameChanged {
		if request.Username == "" {
			err = releaseShareFromUser(path)
		} else {
			err = restrictShareToUser(path, request.Username)
		}

		if err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}
	}

	if err := service.MyService.Shares().UpdateShare(id, request.Username, request.TimeMachine); err != nil {
		if usernameChanged {
			if share.Username == "" {
				_ = releaseShareFromUser(path)
			} else {
				_ = restrictShareToUser(path, share.Username)
			}
		}

		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	share.Username = request.Username
	share.Anonymous = request.Username == ""
	share.TimeMachine = request.TimeMachine

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: share})
}

func DeleteSambaShares(ctx echo.Context) error {
	id := ctx.Param("id")
	if id == "" {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INSUFFICIENT_PERMISSIONS, Message: common_err.GetMsg(common_err.INSUFFICIENT_PERMISSIONS)})
	}
	for _, share := range service.MyService.Shares().GetSharesList() {
		if strconv.FormatUint(uint64(share.ID), 10) == id && share.Username != "" {
			if err := releaseShareFromUser(share.Path); err != nil {
				return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
			}
		}
	}

	if err := service.MyService.Shares().DeleteShare(id); err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: id})
}

// client
func GetSambaConnectionsList(ctx echo.Context) error {
	connections := service.MyService.Connections().GetConnectionsList()
	connectionList := []model.Connections{}
	for _, v := range connections {
		connectionList = append(connectionList, model.Connections{
			ID:         v.ID,
			Username:   v.Username,
			Port:       v.Port,
			Host:       v.Host,
			MountPoint: v.MountPoint,
		})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: connectionList})
}

func PostSambaConnectionsCreate(ctx echo.Context) error {
	connection := model.Connections{}
	ctx.Bind(&connection)
	if connection.Port == "" {
		connection.Port = "445"
	}
	if connection.Username == "" || connection.Host == "" {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CHARACTER_LIMIT, Message: common_err.GetMsg(common_err.CHARACTER_LIMIT)})
	}
	// what the mount would refuse is refused now, before the server is asked or anything is saved
	if _, err := samba.MountOptions(connection.Username, connection.Password, connection.Port); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS), Data: err.Error()})
	}

	// if ok, _ := regexp.MatchString(`^[\w@#*.]{4,30}$`, connection.Password); !ok {
	// 	return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CHARACTER_LIMIT, Message: common_err.GetMsg(common_err.CHARACTER_LIMIT)})
	// 	return
	// }
	// if ok, _ := regexp.MatchString(`^[\w@#*.]{4,30}$`, connection.Username); !ok {
	// 	return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	// 	return
	// }
	// if !ip_helper.IsIPv4(connection.Host) && !ip_helper.IsIPv6(connection.Host) {
	// 	return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	// 	return
	// }
	// if ok, _ := regexp.MatchString("^[0-9]{1,6}$", connection.Port); !ok {
	// 	return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	// 	return
	// }

	connection.Host = strings.Split(connection.Host, "/")[0]
	// check is exists
	connections := service.MyService.Connections().GetConnectionByHost(connection.Host)
	if len(connections) > 0 {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.Record_ALREADY_EXIST, Message: common_err.GetMsg(common_err.Record_ALREADY_EXIST), Data: common_err.GetMsg(common_err.Record_ALREADY_EXIST)})
	}
	// check connect is ok
	directories, err := samba.GetSambaSharesList(connection.Host, connection.Port, connection.Username, connection.Password)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
	}

	connectionDBModel := model2.ConnectionsDBModel{}
	connectionDBModel.Username = connection.Username
	connectionDBModel.Password = connection.Password
	connectionDBModel.Host = connection.Host
	connectionDBModel.Port = connection.Port
	connectionDBModel.Directories = strings.Join(directories, ",")
	baseHostPath := "/mnt/" + connection.Host
	connectionDBModel.MountPoint = baseHostPath
	connection.MountPoint = baseHostPath
	file.IsNotExistMkDir(baseHostPath)
	for _, v := range directories {
		mountPoint := baseHostPath + "/" + v
		file.IsNotExistMkDir(mountPoint)
		service.MyService.Connections().MountSmaba(connectionDBModel.Username, connectionDBModel.Host, v, connectionDBModel.Port, mountPoint, connectionDBModel.Password)
	}

	service.MyService.Connections().CreateConnection(&connectionDBModel)

	connection.ID = connectionDBModel.ID
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: connection})
}

func DeleteSambaConnections(ctx echo.Context) error {
	id := ctx.Param("id")
	connection := service.MyService.Connections().GetConnectionByID(id)
	if connection.Username == "" {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.Record_NOT_EXIST, Message: common_err.GetMsg(common_err.Record_NOT_EXIST)})
	}
	// what is mounted is read from the connection's folder, not asked of the server: a server that is
	// off, or whose password changed, made the connection impossible to delete
	folder := connection.MountPoint
	if folder == "" {
		folder = "/mnt/" + connection.Host
	}
	if err := service.UnmountShares(folder); err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: common_err.GetMsg(common_err.SERVICE_ERROR), Data: err.Error()})
	}
	_ = service.RemoveMountPoint(folder) // once empty
	service.MyService.Connections().DeleteConnection(id)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: id})
}
