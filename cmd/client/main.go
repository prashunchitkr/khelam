package main

import (
	"io"
	"log/slog"
	"net"
	"os"

	"github.com/hashicorp/yamux"
)

const (
	ServerAddr = "127.0.0.1:3101"
	TargetAddr = "127.0.0.1:8000"
)

const (
	ErrCannotListen = 1
)

type khelamClient struct {
	logger  slog.Logger
	session *yamux.Session
}

func main() {
	logHandler := slog.NewTextHandler(os.Stdout, nil)
	logger := slog.New(logHandler)

	rawServerConn, err := net.Dial("tcp", ServerAddr)
	if err != nil {
		logger.Error("Cannot connect to server", "serverAddr", ServerAddr)
		os.Exit(ErrCannotListen)
	}

	logger.Info("Connected to server", "serverAddr", ServerAddr)

	session, err := yamux.Client(rawServerConn, nil)
	if err != nil {
		logger.Error("Cannot start yamux session")
		panic(err)
	}

	client := khelamClient{
		logger:  *logger,
		session: session,
	}

	defer client.stop()
	client.start()
}

func (kc *khelamClient) stop() {
	kc.session.Close()
}

func (kc *khelamClient) start() {
	for {
		conn, err := kc.session.Accept()
		if err != nil {
			kc.logger.Error("Failed to accept connection", "clientAddr", conn.RemoteAddr())
			continue
		}

		go kc.handleConnection(conn)
	}
}

func (kc *khelamClient) handleConnection(srcConn net.Conn) {
	defer srcConn.Close()

	destConn, err := net.Dial("tcp", TargetAddr)
	if err != nil {
		kc.logger.Error("Failed connecting to local service", "serviceAddr", TargetAddr)
		return
	}
	defer destConn.Close()

	kc.logger.Info("Request recieved", "clientAddr", srcConn.RemoteAddr())

	done := make(chan struct{}, 2)

	go func() {
		_, err := io.Copy(destConn, srcConn)
		if err != nil {
			kc.logger.Error("Error copying", "clientAddr", srcConn.RemoteAddr())
		}
		done <- struct{}{}
	}()

	go func() {
		_, err := io.Copy(srcConn, destConn)
		if err != nil {
			kc.logger.Error("Error copying", "clientAddr", srcConn.RemoteAddr())
		}
		done <- struct{}{}
	}()

	<-done
}
