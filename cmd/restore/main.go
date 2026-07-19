package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/ccawmiku/webdav-cold-backup/internal/api"
	"github.com/ccawmiku/webdav-cold-backup/internal/offline"
)

func main() {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	session := &offline.Session{}
	server := &http.Server{Handler: api.NewOffline(session), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 2 * time.Minute}
	address := "http://" + listener.Addr().String()
	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := openBrowser(address); err != nil {
			log.Printf("请手动打开 %s", address)
		}
	}()
	fmt.Printf("WebDAV 冷备份恢复工具已启动：%s\n关闭此窗口即可退出。\n", address)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	_ = server.Shutdown(context.Background())
}

func openBrowser(address string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", address).Start()
	}
	if runtime.GOOS == "darwin" {
		return exec.Command("open", address).Start()
	}
	return exec.Command("xdg-open", address).Start()
}
