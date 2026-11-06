/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-26 11:12:12
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-27 14:58:55
 * @FilePath: /CasaOS/model/share.go
 * @Description:
 * @Website: https://www.casaos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package model

import model2 "github.com/ReCasaOS/CasaOS/service/model"

type Shares struct {
	ID        uint   `json:"id"`
	Anonymous bool   `json:"anonymous"`
	Path      string `json:"path"`
	// Username is the share account allowed to mount a non-anonymous share. It
	// is ignored when Anonymous is true, and when Members is given.
	Username string `json:"username"`
	// Members are the accounts allowed to mount the share, each with write or
	// read-only access. An empty list makes it a guest share; leaving the field
	// out falls back to Username, which is what older clients send.
	Members []model2.ShareMember `json:"members,omitempty"`
	// TimeMachine advertises the share to macOS as a Time Machine destination.
	TimeMachine bool `json:"time_machine"`
}

// UserShare is one share an account may mount, as PUT
// /samba/users/{username}/shares takes it.
type UserShare struct {
	ID       uint `json:"id"`
	ReadOnly bool `json:"read_only"`
}

// SambaUser is the payload for creating a share account or changing its
// password. It is never returned to a client: the password field only ever
// travels inwards.
type SambaUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
