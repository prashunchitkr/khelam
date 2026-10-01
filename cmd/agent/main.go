package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"

	"github.com/prashunchitkr/khelamgg/internal/logger"
	"github.com/prashunchitkr/khelamgg/internal/protocol"
)

// const (
// 	ErrCannotListen = 1
// )
//
// type khelamClient struct {
// 	logger  slog.Logger
// 	cfg     config.AgentConfig
// 	session *yamux.Session
// }
//
// func main() {
// 	logger := logger.New(logger.LoggerOptions{
// 		Writer: os.Stdout,
// 		Level:  slog.LevelInfo,
// 	})
//
// 	agentCfg, err := config.GetAgentConfig(os.Args[1:])
// 	if err != nil {
// 		logger.Error("error while parsing flags", "error", err)
// 		os.Exit(1)
// 	}
//
// 	rawServerConn, err := net.Dial("tcp", agentCfg.ServerAddress)
// 	if err != nil {
// 		logger.Error("Cannot connect to server", "serverAddr", agentCfg.ServerAddress)
// 		os.Exit(ErrCannotListen)
// 	}
//
// 	logger.Info("Connected to server", "serverAddr", agentCfg.ServerAddress)
//
// 	session, err := yamux.Client(rawServerConn, nil)
// 	if err != nil {
// 		logger.Error("Cannot start yamux session")
// 		panic(err)
// 	}
//
// 	client := khelamClient{
// 		logger:  *logger,
// 		cfg:     agentCfg,
// 		session: session,
// 	}
//
// 	defer client.stop()
// 	client.start()
// }
//
// func (kc *khelamClient) stop() {
// 	kc.session.Close()
// }
//
// func (kc *khelamClient) start() {
// 	for {
// 		conn, err := kc.session.Accept()
// 		if err != nil {
// 			kc.logger.Error("Failed to accept connection", "clientAddr", conn.RemoteAddr())
// 			continue
// 		}
//
// 		go kc.handleConnection(conn)
// 	}
// }
//
// func (kc *khelamClient) handleConnection(srcConn net.Conn) {
// 	defer srcConn.Close()
//
// 	destConn, err := net.Dial("tcp", kc.cfg.TunnelAddress)
// 	if err != nil {
// 		kc.logger.Error("Failed connecting to local service", "serviceAddr", kc.cfg.TunnelAddress)
// 		return
// 	}
// 	defer destConn.Close()
//
// 	kc.logger.Info("Request recieved", "clientAddr", srcConn.RemoteAddr())
//
// 	done := make(chan struct{}, 2)
//
// 	go func() {
// 		_, err := io.Copy(destConn, srcConn)
// 		if err != nil {
// 			kc.logger.Error("Error copying", "clientAddr", srcConn.RemoteAddr())
// 		}
// 		done <- struct{}{}
// 	}()
//
// 	go func() {
// 		_, err := io.Copy(srcConn, destConn)
// 		if err != nil {
// 			kc.logger.Error("Error copying", "clientAddr", srcConn.RemoteAddr())
// 		}
// 		done <- struct{}{}
// 	}()
//
// 	<-done
// }

func main() {
	logger := logger.New(logger.LoggerOptions{
		Writer: os.Stdout,
		Level:  slog.LevelInfo,
	})
	serverConnn, err := net.Dial("tcp", "127.0.0.1:30000")
	if err != nil {
		serverConnn.Close()
		panic(err)
	}

	protocol.WriteMessage(serverConnn, protocol.KindRegister, protocol.RegisterMessage{
		Name: "hello",
	})

	for {
		message, err := protocol.ReadMessage(serverConnn)
		if err != nil {
			if errors.Is(err, io.EOF) {
				continue
			}
			logger.Error("error reading message", "error", err)
			continue
		}
		logger.Info("recieved message", "message", message)
	}
}
