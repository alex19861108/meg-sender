package requester

import (
	"io"
	"log"
	"os"
)

var (
	Info    *log.Logger
	Warning *log.Logger
	Error   *log.Logger
)

func init() {
	errFile, err := os.OpenFile("log.stderr", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalln("打开日志文件失败：", err)
	}
	Info = log.New(io.MultiWriter(os.Stdout), "", 0)
	Warning = log.New(io.MultiWriter(os.Stdout), "", 0)
	Error = log.New(io.MultiWriter(os.Stderr, errFile), "", log.Ldate|log.Ltime|log.Lshortfile)
}
