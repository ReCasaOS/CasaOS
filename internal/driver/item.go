/*
 * @Author: a624669980@163.com a624669980@163.com
 * @Date: 2022-12-13 11:05:47
 * @LastEditors: a624669980@163.com a624669980@163.com
 * @LastEditTime: 2022-12-13 11:05:54
 * @FilePath: /drive/internal/driver/item.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
package driver

type Additional interface{}

type IRootPath interface {
	GetRootPath() string
}

type IRootId interface {
	GetRootId() string
}

type RootPath struct {
	RootFolderPath string `json:"root_folder_path"`
}

type RootID struct {
	RootFolderID string `json:"root_folder_id" omit:"true"`
}

func (r RootPath) GetRootPath() string {
	return r.RootFolderPath
}

func (r *RootPath) SetRootPath(path string) {
	r.RootFolderPath = path
}

func (r RootID) GetRootId() string {
	return r.RootFolderID
}
