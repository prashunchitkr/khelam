package main

import (
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
)

const (
	ListenAddr = "0.0.0.0:3300"
	TargetAddr = "127.0.0.1:8000"
)

const (
	ErrCannotListen = 1
)

type client struct {
	logger slog.Logger
	wg     sync.WaitGroup
}

func main() {
	logHandler := slog.NewTextHandler(os.Stdout, nil)
	logger := slog.New(logHandler)

	listener, err := net.Listen("tcp", ListenAddr)
	if err != nil {
		logger.Error("Cannon start listner", "address", ListenAddr)
		os.Exit(ErrCannotListen)
	}

	logger.Info("Proxy Started", "listenAddr", ListenAddr, "targetAddr", TargetAddr)

	proxyClient := client{
		logger: *logger,
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			logger.Error("Failed to accept connection", "clientAddr", conn.RemoteAddr())
			continue
		}

		go proxyClient.handleConnection(conn)
	}
}

func (c *client) handleConnection(srcConn net.Conn) {
	defer srcConn.Close()

	destConn, err := net.Dial("tcp", TargetAddr)
	if err != nil {
		c.logger.Error("Failed connecting to local service", "serviceAddr", TargetAddr)
		return
	}
	defer destConn.Close()

	c.logger.Info("Request recieved", "clientAddr", srcConn.RemoteAddr())
	c.wg.Add(2)

	go func() {
		defer c.wg.Done()
		_, err := io.Copy(destConn, srcConn)
		if err != nil {
			c.logger.Error("Error copying", "clientAddr", srcConn.RemoteAddr(), "listenAddr", ListenAddr)
		}
	}()

	go func() {
		defer c.wg.Done()
		_, err := io.Copy(srcConn, destConn)
		if err != nil {
			c.logger.Error("Error copying", "clientAddr", srcConn.RemoteAddr(), "listenAddr", ListenAddr)
		}
	}()

	c.wg.Wait()
}
