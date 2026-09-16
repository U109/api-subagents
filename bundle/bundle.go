package bundle

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"io/fs"
)

// Data 由构建脚本按发布白名单生成，只含插件和运行必需文件。
//
//go:embed payload.zip
var Data []byte

// Open 直接读取嵌入的插件包，安装时不扫描源码目录或用户配置目录。
func Open() (fs.FS, error) { return zip.NewReader(bytes.NewReader(Data), int64(len(Data))) }
