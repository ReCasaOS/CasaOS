/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-26 18:13:22
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-04 20:10:31
 * @FilePath: /CasaOS/service/connections.go
 * @Description:
 * @Website: https://www.casaos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package service

import (
	"fmt"
	"syscall"

	"github.com/ReCasaOS/CasaOS/pkg/samba"
	"github.com/ReCasaOS/CasaOS/service/model"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
	"github.com/moby/sys/mount"
	"github.com/moby/sys/mountinfo"
	"golang.org/x/sys/unix"
	"gorm.io/gorm"
)

type ConnectionsService interface {
	GetConnectionsList() (connections []model2.ConnectionsDBModel)
	GetConnectionByHost(host string) (connections []model2.ConnectionsDBModel)
	GetConnectionByID(id string) (connections model2.ConnectionsDBModel)
	CreateConnection(connection *model2.ConnectionsDBModel)
	DeleteConnection(id string)
	UpdateConnection(connection *model2.ConnectionsDBModel)
	MountSmaba(username, host, directory, port, mountPoint, password string) error
	UnmountSmaba(mountPoint string) error
}

type connectionsStruct struct {
	db *gorm.DB
}

func (s *connectionsStruct) GetConnectionByHost(host string) (connections []model2.ConnectionsDBModel) {
	s.db.Select("username,host,status,id").Where("host = ?", host).Find(&connections)
	return
}

func (s *connectionsStruct) GetConnectionByID(id string) (connections model2.ConnectionsDBModel) {
	s.db.Select("username,password,host,status,id,directories,mount_point,port").Where("id = ?", id).First(&connections)
	return
}

func (s *connectionsStruct) GetConnectionsList() (connections []model2.ConnectionsDBModel) {
	s.db.Select("username,host,port,status,id,mount_point").Find(&connections)
	return
}

func (s *connectionsStruct) CreateConnection(connection *model2.ConnectionsDBModel) {
	s.db.Create(connection)
}

func (s *connectionsStruct) UpdateConnection(connection *model2.ConnectionsDBModel) {
	s.db.Save(connection)
}

func (s *connectionsStruct) DeleteConnection(id string) {
	s.db.Where("id= ?", id).Delete(&model.ConnectionsDBModel{})
}

func (s *connectionsStruct) MountSmaba(username, host, directory, port, mountPoint, password string) error {
	options, err := samba.MountOptions(username, password, port)
	if err != nil {
		return err
	}
	err = unix.Mount(
		fmt.Sprintf("//%s/%s", host, directory),
		mountPoint,
		"cifs",
		unix.MS_NOATIME|unix.MS_NODEV|unix.MS_NOSUID,
		options,
	)
	return err
	// str := command2.ExecResultStr("source " + config.AppInfo.ShellPath + "/helper.sh ;MountCIFS " + username + " " + host + " " + directory + " " + port + " " + mountPoint + " " + password)
	// return str
}

func (s *connectionsStruct) UnmountSmaba(mountPoint string) error {
	return mount.Unmount(mountPoint)
}

func NewConnectionsService(db *gorm.DB) ConnectionsService {
	return &connectionsStruct{db: db}
}

// RemoveMountPoint removes the folder a filesystem was mounted on, after its unmount: only when it is no
// longer a mount point, and only when it is an empty folder (rmdir, never a file, never what is inside).
// What a mount that is still there holds (busy, or its unmount failed) is never touched: on a network
// share those files are the server's.
func RemoveMountPoint(dir string) error {
	mounted, err := isMountPoint(dir)
	if err != nil {
		return err
	}
	if mounted {
		return fmt.Errorf("%s is still a mount point", dir)
	}
	return syscall.Rmdir(dir)
}

// isMountPoint compares the folder's device with its parent's, or reads /proc/self/mountinfo; a var, so
// that tests can say a folder is still mounted.
var isMountPoint = mountinfo.Mounted
