package appmain

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"media-dedupe/frontend"
	"media-dedupe/internal/appapi"
)

// Run boots the Wails desktop app.
func Run() {
	app := appapi.NewApp()

	assets, err := frontend.FS()
	if err != nil {
		log.Fatalf("load frontend: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("media listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.Handle("/media/", app)
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(ln); err != nil {
			log.Printf("media server stopped: %v", err)
		}
	}()
	app.SetMediaBase(fmt.Sprintf("http://127.0.0.1:%d/media", port))

	var appCtx context.Context
	app.SetEmitter(func(event string, payload any) {
		if appCtx == nil {
			return
		}
		runtime.EventsEmit(appCtx, event, payload)
	})

	err = wails.Run(&options.App{
		Title:     "文件去重助手",
		Width:     1280,
		Height:    860,
		MinWidth:  980,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 241, G: 245, B: 249, A: 1},
		OnStartup: func(ctx context.Context) {
			appCtx = ctx
			app.Startup(ctx)
		},
		Bind: []any{app},
		OnShutdown: func(ctx context.Context) {
			app.CleanupUpdater()
			_ = srv.Close()
		},
	})
	if err != nil {
		log.Println(err)
	}
	_ = srv.Close()
}
