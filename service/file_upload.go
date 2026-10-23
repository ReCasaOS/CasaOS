package service

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"sync"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

type FileInfo struct {
	init             bool
	uploaded         []bool
	uploadedChunkNum int64
}

// ErrInvalidChunk is a chunk number outside 1..totalChunks, a chunk count the file's size cannot have, or one that differs from the upload's first chunk.
var ErrInvalidChunk = errors.New("invalid chunk number")

// maxChunks bounds the chunk table: 16 TiB in the dashboard's 1 MiB chunks.
const maxChunks = 1 << 24

type FileUploadService struct {
	uploadStatus sync.Map
	lock         sync.RWMutex
}

func NewFileUploadService() *FileUploadService {
	return &FileUploadService{
		uploadStatus: sync.Map{},
		lock:         sync.RWMutex{},
	}
}

func (s *FileUploadService) TestChunk(
	c echo.Context,
	identifier string,
	chunkNumber int64,
) error {
	fileInfoTemp, ok := s.uploadStatus.Load(identifier)

	if !ok {
		return fmt.Errorf("file not found")
	}

	fileInfo := fileInfoTemp.(*FileInfo)

	if !fileInfo.init {
		return fmt.Errorf("file not init")
	}

	s.lock.RLock()
	defer s.lock.RUnlock()
	if chunkNumber < 1 || chunkNumber > int64(len(fileInfo.uploaded)) {
		return ErrInvalidChunk
	}
	// return StatusNoContent instead of 404
	// the is require by frontend
	if !fileInfo.uploaded[chunkNumber-1] {
		return fmt.Errorf("file not found")
	}

	return nil
}

func (s *FileUploadService) UploadFile(
	c echo.Context,
	path string,
	chunkNumber int64,
	chunkSize int64,
	currentChunkSize int64,
	totalChunks int64,
	totalSize int64,
	identifier string,
	relativePath string,
	fileName string,
	bin *multipart.FileHeader,
) error {
	// a client never sends more chunks than the size holds (its last chunk takes the remainder): more would size the table from nothing
	if totalChunks < 1 || totalChunks > maxChunks || chunkNumber < 1 || chunkNumber > totalChunks || chunkSize < 1 || totalSize < 0 || totalChunks > totalSize/chunkSize+1 {
		return ErrInvalidChunk
	}
	s.lock.Lock()
	fileInfoTemp, ok := s.uploadStatus.Load(identifier)
	var fileInfo *FileInfo
	// a chunk count unlike the first chunk's would index past what is recorded and leave the uploads locked
	if ok && int64(len(fileInfoTemp.(*FileInfo).uploaded)) != totalChunks {
		s.lock.Unlock()
		return ErrInvalidChunk
	}

	if relativePath != fileName {
		// uploaded file is folder
		folderPath := filepath.Dir(path + "/" + relativePath)
		if _, err := os.Stat(folderPath); os.IsNotExist(err) {
			err := os.MkdirAll(folderPath, os.ModePerm)
			if err != nil {
				s.lock.Unlock()
				return err
			}
		}
	}

	file, err := os.OpenFile(path+"/"+relativePath+".tmp", os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		s.lock.Unlock()
		return err
	}
	defer file.Close()

	if !ok {
		// pre allocate file size, and drop what an older upload left in the .tmp
		if err := file.Truncate(totalSize); err != nil {
			s.lock.Unlock()
			return err
		}

		// file info init
		fileInfo = &FileInfo{
			init:             true,
			uploaded:         make([]bool, totalChunks),
			uploadedChunkNum: 0,
		}
		s.uploadStatus.Store(identifier, fileInfo)
	} else {
		fileInfo = fileInfoTemp.(*FileInfo)
	}

	s.lock.Unlock()

	_, err = file.Seek((chunkNumber-1)*chunkSize, io.SeekStart)
	if err != nil {
		return err
	}

	src, err := bin.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	_, err = io.Copy(file, src)

	if err != nil {
		fmt.Println(err)
		return err
	}

	s.lock.Lock()
	// handle file after write a chunk
	// handle single chunk upload twice
	if !fileInfo.uploaded[chunkNumber-1] {
		fileInfo.uploadedChunkNum++
		fileInfo.uploaded[chunkNumber-1] = true
	}

	// handle file after write all chunk
	if fileInfo.uploadedChunkNum == totalChunks {
		err := file.Close()
		if err != nil {
			logger.Error("close file error: ", zap.Error(err))
		}

		err = os.Rename(path+"/"+relativePath+".tmp", path+"/"+relativePath)
		if err != nil {
			logger.Error("rename file error: ", zap.Error(err))
		}
		// remove upload status info after upload complete
		s.uploadStatus.Delete(identifier)
	}
	s.lock.Unlock()

	return nil
}
