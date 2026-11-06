/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-26 11:17:17
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-27 15:25:07
 * @FilePath: /CasaOS/service/model/o_shares.go
 * @Description:
 * @Website: https://www.casaos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package model

import "strings"

type SharesDBModel struct {
	ID        uint `gorm:"column:id;primary_key" json:"id"`
	Anonymous bool `json:"anonymous"`
	// Username is the account that owns the shared directory: every session of a
	// protected share runs as it, whoever connected. Empty means the share is
	// open to guests, which is what every row created before authenticated
	// shares existed carries, so an upgrade leaves them untouched.
	Username string `json:"username"`
	// Members lists the accounts allowed to mount a protected share and what
	// each may do, as "alice:rw bob:ro". Rows from before members existed have
	// it empty and a Username: their one account keeps reading and writing.
	Members string `json:"members"`
	// TimeMachine adds Apple's SMB extensions to this share's section so macOS
	// offers it as a Time Machine destination. AutoMigrate adds the column, and
	// rows predating it read back as false, i.e. the section they already had.
	TimeMachine bool   `json:"time_machine"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	Updated     int64  `gorm:"autoUpdateTime"`
	Created     int64  `gorm:"autoCreateTime"`
}

func (p *SharesDBModel) TableName() string {
	return "o_shares"
}

// ShareMember is one account allowed to mount a protected share.
type ShareMember struct {
	Username string `json:"username"`
	ReadOnly bool   `json:"read_only"`
}

// MemberList returns who may mount the share. A guest share has nobody, and a
// row from before members existed has its one account, with write access.
func (p *SharesDBModel) MemberList() []ShareMember {
	members := []ShareMember{}

	for _, field := range strings.Fields(p.Members) {
		name, access, _ := strings.Cut(field, ":")
		members = append(members, ShareMember{Username: name, ReadOnly: access == "ro"})
	}

	if len(members) == 0 && p.Username != "" {
		members = append(members, ShareMember{Username: p.Username})
	}

	return members
}

// FormatMembers is the inverse of MemberList, for the Members column.
func FormatMembers(members []ShareMember) string {
	fields := make([]string, 0, len(members))

	for _, member := range members {
		access := "rw"
		if member.ReadOnly {
			access = "ro"
		}

		fields = append(fields, member.Username+":"+access)
	}

	return strings.Join(fields, " ")
}
