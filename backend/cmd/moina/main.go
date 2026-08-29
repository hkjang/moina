package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hkjang/moina/backend/internal/config"
	"github.com/hkjang/moina/backend/internal/httpapi"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/hkjang/moina/backend/internal/store"
	"golang.org/x/crypto/bcrypt"
)

var version = "dev"

const (
	listenAddress = ":8080"
	privateCAPath = "/etc/moina/certs/ca-certificates.crt"
	systemCAPath  = "/etc/ssl/certs/ca-certificates.crt"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("서비스 종료", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	repo, err := store.Open(ctx, cfg.PostgresDSN)
	cancel()
	if err != nil {
		return fmt.Errorf("PostgreSQL 초기화 실패: %w", err)
	}
	defer repo.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.BootstrapPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("bootstrap 비밀번호 처리 실패: %w", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	err = repo.BootstrapAdmin(ctx, cfg.BootstrapAdmin, string(hash))
	cancel()
	if err != nil {
		return fmt.Errorf("bootstrap 관리자 초기화 실패: %w", err)
	}
	secrets, err := secure.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	api := httpapi.New(repo, secrets, version)
	client, err := outboundHTTPClient()
	if err != nil {
		return err
	}
	api.SetHTTPClient(client)

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("MOINA 시작", "version", version, "address", listenAddress)
		serverErrors <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	select {
	case received := <-signals:
		slog.Info("종료 신호 수신", "signal", received.String())
	case serveErr := <-serverErrors:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	return server.Shutdown(shutdownCtx)
}

func outboundHTTPClient() (*http.Client, error) {
	pool := x509.NewCertPool()
	if pem, readErr := os.ReadFile(systemCAPath); readErr == nil {
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("시스템 CA 파일에 유효한 인증서가 없습니다")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("시스템 CA 파일 읽기 실패: %w", readErr)
	}
	if pem, readErr := os.ReadFile(privateCAPath); readErr == nil {
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("private CA 파일에 유효한 인증서가 없습니다")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("private CA 파일 읽기 실패: %w", readErr)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	return &http.Client{Transport: transport}, nil
}

func healthcheck() error {
	client := &http.Client{Timeout: 4 * time.Second}
	response, err := client.Get("http://127.0.0.1:8080/readyz")
	if err != nil {
		return fmt.Errorf("readyz 연결 실패: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz 응답 상태: %s", response.Status)
	}
	return nil
}
