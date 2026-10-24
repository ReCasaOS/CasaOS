/*
 * @Author: LinkLeong link@icewhale.org
 * @Date: 2022-07-26 11:21:14
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-08-18 11:16:25
 * @FilePath: /CasaOS/service/shares.go
 * @Description:
 * @Website: https://www.casaos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package service

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/ReCasaOS/CasaOS-Common/utils/command"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"github.com/ReCasaOS/CasaOS/service/model"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type SharesService interface {
	GetSharesList() (shares []model2.SharesDBModel)
	GetSharesByPath(path string) (shares []model2.SharesDBModel)
	GetSharesByName(name string) (shares []model2.SharesDBModel)
	CreateShare(share model2.SharesDBModel) error
	GetShareByID(id string) (share model2.SharesDBModel, found bool)
	UpdateShare(id string, username string, timeMachine bool) error
	DeleteShare(id string) error
	UpdateConfigFile() error
	InitSambaConfig()
	DeleteShareByPath(path string) error
}

type sharesStruct struct {
	db *gorm.DB

	// mu serialises share changes: config write + smbd refresh + DB commit. The
	// shares to close are derived from the file before/after, so an interleaved
	// edit could skip closing a share whose access was narrowed.
	mu sync.Mutex

	// restartSMBD restarts smbd; reloadSMBD reloads it, then closes the named
	// shares (shares_refresh.go). Tests replace both; nil reloadSMBD → restart.
	restartSMBD func() error
	reloadSMBD  func(closeShares []string) error
}

func (s *sharesStruct) DeleteShareByPath(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.db.Where("path LIKE ?", path+"%").Delete(&model.SharesDBModel{})

	return s.updateConfigFile()
}

func (s *sharesStruct) GetSharesByName(name string) (shares []model2.SharesDBModel) {
	s.db.Where("name = ?", name).Find(&shares)

	return
}

func (s *sharesStruct) GetSharesByPath(path string) (shares []model2.SharesDBModel) {
	s.db.Where("path = ?", path).Find(&shares)
	return
}

func (s *sharesStruct) GetSharesList() (shares []model2.SharesDBModel) {
	s.db.Find(&shares)
	return
}

func (s *sharesStruct) CreateShare(share model2.SharesDBModel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mainWritten := s.initSambaConfig()

	// Write and validate the configuration this share would produce before the
	// row is committed. Inserting first would leave an unusable row behind when
	// Samba rejects the result, and every later share operation would then fail
	// on that same row.
	shares := []model2.SharesDBModel{}
	s.db.Find(&shares)

	if err := s.applyConfig(append(shares, share), mainWritten); err != nil {
		return err
	}

	s.db.Create(&share)

	return nil
}

func (s *sharesStruct) GetShareByID(id string) (share model2.SharesDBModel, found bool) {
	result := s.db.Where("id = ?", id).First(&share)

	return share, result.Error == nil
}

// UpdateShare moves a share between guest access and a named account, in either
// direction, and sets its Time Machine flag. An empty username makes it a guest
// share again.
//
// As with creation, the configuration is written and accepted before the row is
// changed, so a rejected result leaves the share exactly as it was rather than
// half converted.
func (s *sharesStruct) UpdateShare(id string, username string, timeMachine bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	shares := []model2.SharesDBModel{}
	s.db.Find(&shares)

	for i := range shares {
		if strconv.FormatUint(uint64(shares[i].ID), 10) == id {
			shares[i].Username = username
			shares[i].Anonymous = username == ""
			shares[i].TimeMachine = timeMachine
		}
	}

	if err := s.applyConfig(shares, false); err != nil {
		return err
	}

	return s.db.Model(&model.SharesDBModel{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{"username": username, "anonymous": username == "", "time_machine": timeMachine}).
		Error
}

func (s *sharesStruct) DeleteShare(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.db.Where("id= ?", id).Delete(&model.SharesDBModel{})

	return s.updateConfigFile()
}

func (s *sharesStruct) UpdateConfigFile() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.updateConfigFile()
}

func (s *sharesStruct) updateConfigFile() error {
	shares := []model2.SharesDBModel{}
	s.db.Find(&shares)

	return s.applyConfig(shares, false)
}

// applyConfig writes the share configuration, has Samba check it, and has smbd
// take it only once it has been accepted: a restart when smb.conf changed
// (mainChanged, or the guest mapping flipped here), otherwise a reload
// (refreshSamba).
func (s *sharesStruct) applyConfig(shares []model2.SharesDBModel, mainChanged bool) error {
	configStr := ""
	hasProtectedShare := false

	if err := checkExternalMainConfig(); err != nil {
		return err
	}

	for _, share := range shares {
		configStr += sambaSection(share)

		if share.Username != "" {
			hasProtectedShare = true
		}
	}

	if mappingChanged, err := syncGuestMapping(hasProtectedShare); err != nil {
		logger.Error("failed to update the samba guest mapping", zap.Error(err))
	} else if mappingChanged {
		mainChanged = true
	}

	previous, previousErr := os.ReadFile(sambaShareConfigFile)

	if err := file.WriteToPath([]byte(configStr), sambaConfigDir, sambaShareConfigName); err != nil {
		return err
	}

	if err := validateSambaConfig(); err != nil {
		// Leave smbd pointed at something it will accept. With no previous file
		// to restore, removing ours is the correct undo: smb.conf includes it,
		// and Samba ignores an include that does not exist.
		if previousErr == nil {
			if restoreErr := file.WriteToPath(previous, sambaConfigDir, sambaShareConfigName); restoreErr != nil {
				logger.Error("failed to restore the previous share configuration", zap.Error(restoreErr))
			}
		} else if removeErr := os.Remove(sambaShareConfigFile); removeErr != nil {
			logger.Error("failed to remove the rejected share configuration", zap.Error(removeErr))
		}

		return err
	}

	if previousErr != nil {
		previous = nil
	}
	if err := s.refreshSamba(mainChanged, previous, []byte(configStr)); err != nil {
		logger.Error("failed to apply the share configuration to smbd", zap.Error(err))
	}

	return nil
}

func restartSambaService() error {
	if output, err := command.OnlyExec("source " + config.AppInfo.ShellPath + "/helper.sh ;RestartSMBD"); err != nil {
		return fmt.Errorf("restart samba: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func (s *sharesStruct) InitSambaConfig() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.initSambaConfig()
}

// initSambaConfig writes CasaOS' smb.conf where there is none of CasaOS';
// reports whether it did.
func (s *sharesStruct) initSambaConfig() bool {
	if SambaMainConfigExternal() {
		return false
	}
	if file.Exists(sambaConfigFile) {
		str := file.ReadLine(1, sambaConfigFile)
		if strings.Contains(str, "# Copyright (c) 2021-2022 CasaOS Inc. All rights reserved.") {
			// The guest mapping of an already-configured host is kept in step by
			// applyConfig, which knows whether any share is actually protected.
			return false
		}
		file.MoveFile("/etc/samba/smb.conf", "/etc/samba/smb.conf.bak")
		smbConf := ""
		smbConf += `# Copyright (c) 2021-2022 CasaOS Inc. All rights reserved.
#
#
#                          ______     _______
#                        (  __  \   (  ___  )
#                        | (  \  )  | (   ) |
#                        | |   ) |  | |   | |
#                        | |   | |  | |   | |
#                        | |   ) |  | |   | |
#                        | (__/  )  | (___) |
#                        (______/   (_______)
#
#                   _          _______   _________
#                  ( (    /|  (  ___  )  \__   __/
#                  |  \  ( |  | (   ) |     ) (
#                  |   \ | |  | |   | |     | |
#                  | (\ \) |  | |   | |     | |
#                  | | \   |  | |   | |     | |
#                  | )  \  |  | (___) |     | |
#                  |/    )_)  (_______)     )_(
#
#   _______    _______    ______    _________   _______
#  (       )  (  ___  )  (  __  \   \__   __/  (  ____ \  |\     /|
#  | () () |  | (   ) |  | (  \  )     ) (     | (    \/  ( \   / )
#  | || || |  | |   | |  | |   ) |     | |     | (__       \ (_) /
#  | |(_)| |  | |   | |  | |   | |     | |     |  __)       \   /
#  | |   | |  | |   | |  | |   ) |     | |     | (           ) (
#  | )   ( |  | (___) |  | (__/  )  ___) (___  | )           | |
#  |/     \|  (_______)  (______/   \_______/  |/            \_/
#
#
# IMPORTANT: CasaOS will not provide technical support for any issues
#            caused by unauthorized modification to the configuration.

[global]
## fruit settings
   min protocol = SMB2
   ea support = yes
## vfs objects = fruit streams_xattr
   fruit:metadata = stream
   fruit:model = Macmini
   fruit:veto_appledouble = no
   fruit:posix_rename = yes
   fruit:zero_file_id = yes
   fruit:wipe_intentionally_left_blank_rfork = yes
   fruit:delete_empty_adfiles = yes
   multicast dns register = yes
   map to guest = never
   include=/etc/samba/smb.casa.conf`
		file.WriteToPath([]byte(smbConf), "/etc/samba", "smb.conf")
		return true
	}
	return false
}

func NewSharesService(db *gorm.DB) SharesService {
	return &sharesStruct{
		db:          db,
		restartSMBD: restartSambaService,
		reloadSMBD:  reloadSambaService,
	}
}
