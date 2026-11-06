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
			Members:     v.MemberList(),
			TimeMachine: v.TimeMachine,
		})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: shareList})
}

func PostSambaSharesCreate(ctx echo.Context) error {
	sharesMutex.Lock()
	defer sharesMutex.Unlock()

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
	memberLists := make([][]model2.ShareMember, len(shares))

	for i, v := range shares {
		if v.Path == "" {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INSUFFICIENT_PERMISSIONS, Message: common_err.GetMsg(common_err.INSUFFICIENT_PERMISSIONS)})
		}

		// Sharing a folder changes its ownership and permissions as root, so the
		// path is confined before anything touches it.
		path, err := service.ValidateNewSharePath(v.Path)
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

		// A share is protected when it names accounts, not when a boolean says
		// so. Clients that predate this field omit it and keep getting the guest
		// share they have always got.
		members, err := resolveShareMembers(v)
		if err != nil {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
		}

		memberLists[i] = members

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
		members := memberLists[i]
		owner := chooseShareOwner("", members, "")

		shareDBModel := model2.SharesDBModel{
			Anonymous:   owner == "",
			Username:    owner,
			Members:     model2.FormatMembers(members),
			Path:        path,
			Name:        filepath.Base(path),
			TimeMachine: v.TimeMachine,
		}

		shares[i].Username = owner
		shares[i].Members = members
		shares[i].Anonymous = owner == ""

		if owner == "" {
			// Unchanged from before authenticated shares existed: a guest share has
			// no owner to speak of, so the directory stays world-writable.
			os.Chmod(path, 0o777)
		} else if err := service.RestrictShareToUser(path, owner); err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}

		if err := service.MyService.Shares().CreateShare(shareDBModel); err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: shares})
}

// PutSambaShare sets who may mount an existing share, moving it between guest
// access and named accounts in either direction, and turns its Time Machine
// flag on or off. An empty member list makes it a guest share again; a body
// with a username and no members is what older clients send, and means that
// one account with write access.
//
// The body replaces all of it, so a client that omits time_machine clears it.
//
// Moving between guest access and accounts re-owns only the shared directory
// itself, never its contents. A media
// share can hold millions of files and walking them inside an HTTP request is
// not something a NAS should be asked to do. It is not needed either: files
// created under a guest share are world-readable, and files created under a
// protected share stay reachable by root, which is who a guest session maps to.
func PutSambaShare(ctx echo.Context) error {
	sharesMutex.Lock()
	defer sharesMutex.Unlock()

	id := ctx.Param("id")

	share, found := service.MyService.Shares().GetShareByID(id)
	if !found {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: "no such share"})
	}

	request := model.Shares{}
	if err := ctx.Bind(&request); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	members, err := resolveShareMembers(request)
	if err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	if model2.FormatMembers(members) == model2.FormatMembers(share.MemberList()) && request.TimeMachine == share.TimeMachine {
		return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: shareResponse(share)})
	}

	if request.TimeMachine {
		if err := service.ValidateTimeMachineSupport(); err != nil {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
		}
	}

	// Ownership moves first. If Samba then refuses the configuration the share
	// keeps working as it did, and the directory is put back. A request that
	// only flips the Time Machine flag leaves the directory alone:
	// ReleaseShareFromUser would drop a guest folder from 0777 to 0755.
	share, err = applyShareMembers(share, members, request.TimeMachine, "")
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: shareResponse(share)})
}

// shareResponse is a share as the API returns it.
func shareResponse(share model2.SharesDBModel) model.Shares {
	return model.Shares{
		ID:          share.ID,
		Anonymous:   share.Anonymous,
		Path:        share.Path,
		Username:    share.Username,
		Members:     share.MemberList(),
		TimeMachine: share.TimeMachine,
	}
}

// pathSharedElsewhere says whether another share row names the same folder, as
// the duplicates an unserialised create could leave behind do. Deleting one of
// those must not hand the folder back to root under the one that stays.
func pathSharedElsewhere(shares []model2.SharesDBModel, share model2.SharesDBModel) bool {
	for _, other := range shares {
		if other.ID != share.ID && other.Path == share.Path {
			return true
		}
	}

	return false
}

func DeleteSambaShares(ctx echo.Context) error {
	sharesMutex.Lock()
	defer sharesMutex.Unlock()

	id := ctx.Param("id")
	if id == "" {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INSUFFICIENT_PERMISSIONS, Message: common_err.GetMsg(common_err.INSUFFICIENT_PERMISSIONS)})
	}
	shares := service.MyService.Shares().GetSharesList()
	for _, share := range shares {
		if strconv.FormatUint(uint64(share.ID), 10) == id && share.Username != "" && !pathSharedElsewhere(shares, share) {
			if err := service.ReleaseShareFromUser(share.Path); err != nil {
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
