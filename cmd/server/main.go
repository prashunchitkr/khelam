package main

import (
	"io"
	"log/slog"
	"net"
	"os"

	"github.com/hashicorp/yamux"
	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/logger"
)

const (
	PublicListenAddr = "0.0.0.0:3100"
)

type khelamServer struct {
	logger        slog.Logger
	session       *yamux.Session
	publicListner net.Listener
}

func main() {
	logger := logger.New(logger.LoggerOptions{
		Writer: os.Stdout,
		Level:  slog.LevelInfo,
	})

	serverConfig, err := config.GetServerConfig(os.Args[1:])
	if err != nil {
		logger.Error("error when parsing flags", "error", err)
		os.Exit(1)
	}

	clientListener, err := net.Listen("tcp", serverConfig.ControlAddress)
	if err != nil {
		logger.Error("Cannot listen for client", "clientAddr", serverConfig.ControlAddress)
		os.Exit(-1)
	}

	logger.Info("Listening for clients", "clientAddr", serverConfig.ControlAddress)

	rawClientConn, err := clientListener.Accept()
	if err != nil {
		logger.Error("Failed to listen to client connection", "clientAddr", rawClientConn.RemoteAddr())
		os.Exit(-2)
	}
	logger.Info("Client connected", "clientAddr", rawClientConn.RemoteAddr())

	session, err := yamux.Server(rawClientConn, nil)
	if err != nil {
		logger.Error("Cannot start yamux server")
		panic(err)
	}

	publicListner, err := net.Listen("tcp", PublicListenAddr)
	if err != nil {
		logger.Error("Cannot listen for public connections", "publicAddr", PublicListenAddr)
		os.Exit(-3)
	}

	server := khelamServer{
		logger:        *logger,
		session:       session,
		publicListner: publicListner,
	}

	defer func() {
		server.stop()
		clientListener.Close()
	}()

	server.start()
}

func (ks *khelamServer) start() {
	for {
		publicUserConn, err := ks.publicListner.Accept()
		if err != nil {
			ks.logger.Error("Cannot connect to user", "userAddr", publicUserConn.RemoteAddr())
			continue
		}

		go ks.handleUserConnecton(publicUserConn)
	}
}

func (ks *khelamServer) stop() {
	ks.session.Close()
	ks.publicListner.Close()
}

func (ks *khelamServer) handleUserConnecton(conn net.Conn) {
	defer conn.Close()
	stream, err := ks.session.Open()
	if err != nil {
		ks.logger.Error("Cannot open stream", "userAddr", stream.RemoteAddr())
		return
	}
	defer stream.Close()

	done := make(chan struct{}, 2)

	go func() {
		io.Copy(stream, conn)
		done <- struct{}{}
	}()

	go func() {
		io.Copy(conn, stream)
		done <- struct{}{}
	}()

	<-done
}
