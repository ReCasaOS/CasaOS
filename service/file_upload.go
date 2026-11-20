package service

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// uploadKey names an upload in progress. The dashboard's identifier is the file's size and name,
// so the same file sent to two folders carries it twice: the file it lands in tells them apart.
type uploadKey struct{ identifier, target string }

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
	target string,
	chunkNumber int64,
) error {
	fileInfoTemp, ok := s.uploadStatus.Load(uploadKey{identifier, target})

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

// uploadFS is where an upload writes: the host's paths for the owner, the os.Root of an
// account's folder for an account (CasaOS#168), whose every name the kernel resolves
// relative to that folder.
type uploadFS interface {
	Stat(name string) (fs.FileInfo, error)
	MkdirAll(name string, perm fs.FileMode) error
	OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error)
	Rename(oldname, newname string) error
}

type hostFS struct{}

func (hostFS) Stat(name string) (fs.FileInfo, error)        { return os.Stat(name) }
func (hostFS) MkdirAll(name string, perm fs.FileMode) error { return os.MkdirAll(name, perm) }
func (hostFS) Rename(oldname, newname string) error         { return os.Rename(oldname, newname) }
func (hostFS) OpenFile(name string, flag int, perm fs.FileMode) (*os.File, error) {
	return os.OpenFile(name, flag, perm)
}

// UploadFile writes a chunk of an upload into the folder path, by the host's paths.
func (s *FileUploadService) UploadFile(c echo.Context, path string, chunkNumber, chunkSize, currentChunkSize, totalChunks, totalSize int64, identifier, relativePath, fileName string, bin *multipart.FileHeader) error {
	return s.upload(c, hostFS{}, path, path, chunkNumber, chunkSize, currentChunkSize, totalChunks, totalSize, identifier, relativePath, fileName, bin)
}

// UploadFileIn is UploadFile for an account: the folder path is rel below root, and every
// file of the upload is made through root.
func (s *FileUploadService) UploadFileIn(c echo.Context, root *os.Root, rel, path string, chunkNumber, chunkSize, currentChunkSize, totalChunks, totalSize int64, identifier, relativePath, fileName string, bin *multipart.FileHeader) error {
	return s.upload(c, root, rel, path, chunkNumber, chunkSize, currentChunkSize, totalChunks, totalSize, identifier, relativePath, fileName, bin)
}

func (s *FileUploadService) upload(
	c echo.Context,
	fsys uploadFS,
	base string,
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
	key := uploadKey{identifier, path + "/" + relativePath}
	s.lock.Lock()
	fileInfoTemp, ok := s.uploadStatus.Load(key)
	var fileInfo *FileInfo
	// a chunk count unlike the first chunk's would index past what is recorded and leave the uploads locked
	if ok && int64(len(fileInfoTemp.(*FileInfo).uploaded)) != totalChunks {
		s.lock.Unlock()
		return ErrInvalidChunk
	}

	if relativePath != fileName {
		// uploaded file is folder
		folderPath := filepath.Dir(filepath.Join(base, relativePath))
		if _, err := fsys.Stat(folderPath); os.IsNotExist(err) {
			err := fsys.MkdirAll(folderPath, os.ModePerm)
			if err != nil {
				s.lock.Unlock()
				return err
			}
		}
	}

	file, err := fsys.OpenFile(filepath.Join(base, relativePath)+".tmp", os.O_WRONLY|os.O_CREATE, 0644)
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
		s.uploadStatus.Store(key, fileInfo)
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
	defer s.lock.Unlock()
	// handle file after write a chunk
	// a chunk sent twice counts once, and ends nothing: its first copy did, or will
	if fileInfo.uploaded[chunkNumber-1] {
		return nil
	}
	fileInfo.uploadedChunkNum++
	fileInfo.uploaded[chunkNumber-1] = true

	// handle file after write all chunk
	if fileInfo.uploadedChunkNum == totalChunks {
		// the upload is over, done or not: a file that could not take its name is sent again from its first chunk
		s.uploadStatus.Delete(key)

		// the answer to the last chunk is the answer to the upload: it says when the file is not there
		if err := file.Close(); err != nil {
			logger.Error("close file error: ", zap.Error(err))
			return err
		}
		if err := fsys.Rename(filepath.Join(base, relativePath)+".tmp", filepath.Join(base, relativePath)); err != nil {
			logger.Error("rename file error: ", zap.Error(err))
			return err
		}
	}

	return nil
}
