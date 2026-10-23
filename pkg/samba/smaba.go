/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-27 10:35:29
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-01 13:56:44
 * @FilePath: /CasaOS/pkg/samba/smaba.go
 * @Description:
 * @Website: https://www.casaos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package samba

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"
)

func ConnectSambaService(host, port, username, password, directory string) error {
	conn, err := net.Dial("tcp", host+":"+port)
	if err != nil {
		return err
	}
	defer conn.Close()
	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     username,
			Password: password,
		},
	}

	s, err := d.Dial(conn)
	if err != nil {
		return err
	}
	defer s.Logoff()
	names, err := s.ListSharenames()
	if err != nil {
		return err
	}

	for _, name := range names {
		if name == directory {
			return nil
		}
	}
	return errors.New("directory not found")
}

// get share name list
func GetSambaSharesList(host, port, username, password string) ([]string, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     username,
			Password: password,
		},
	}

	s, err := d.Dial(conn)
	if err != nil {
		return nil, err
	}
	defer s.Logoff()
	names, err := s.ListSharenames()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(names, unsafeShare), nil
}

// timeout bounds the dial, then the SMB handshake and the share list: a host that accepts and never
// answers held the remount at boot, and the storages after it, forever. A var, so tests can shorten it.
var timeout = 15 * time.Second

// unsafeShare says whether a share name from the server would not be one folder under /mnt/<host>.
func unsafeShare(name string) bool {
	return name == "" || name == "." || name == ".." || strings.Contains(name, "/")
}

// MountOptions are the options of the CIFS mount of a share: the credentials, and the connection's port.
// The kernel splits them at each comma and reads ",," as a comma within the password, the escape that
// mount.cifs itself uses, so a password may hold any comma. A user name or a port cannot be escaped that
// way: one that holds a separator is refused, and so is a port that is not a number.
func MountOptions(username, password, port string) (string, error) {
	if strings.ContainsAny(username, ",\r\n\x00") {
		return "", errors.New("the user name holds a comma or a line break")
	}
	options := "username=" + username + ",password=" + strings.ReplaceAll(password, ",", ",,")
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return "", fmt.Errorf("the port %q is not a number", port)
		}
		options += ",port=" + strconv.FormatUint(n, 10) // the kernel reads "0445" as octal
	}
	return options, nil
}
