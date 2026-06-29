package service

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
)

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	FromAddr string
	UseTLS   bool
	Subject  string // verification-code email subject (empty → default)
}

type SMTPService struct{}

func NewSMTPService() *SMTPService {
	return &SMTPService{}
}

func (s *SMTPService) SendCode(ctx context.Context, cfg SMTPConfig, to, code, purpose string) error {
	_ = ctx
	if strings.TrimSpace(cfg.Host) == "" || cfg.Port <= 0 || strings.TrimSpace(cfg.FromAddr) == "" {
		return errors.New("SMTP 未配置")
	}
	action := "注册"
	if purpose == "reset" {
		action = "找回密码"
	}
	subject := strings.TrimSpace(cfg.Subject)
	if subject == "" {
		subject = "Vivid AI 邮箱验证码"
	}
	body := fmt.Sprintf("你正在进行%s，验证码为：%s\n\n验证码 6 分钟内有效。", action, code)
	msg := buildSMTPMessage(cfg.FromAddr, to, subject, body)
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	if cfg.UseTLS || cfg.Port == 465 {
		return sendMailTLS(addr, cfg, to, msg)
	}
	return sendMailSTARTTLS(addr, cfg, to, msg)
}

func buildSMTPMessage(from, to, subject, body string) []byte {
	lines := []string{
		"From: " + from,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}
	return []byte(strings.Join(lines, "\r\n"))
}

func sendMailTLS(addr string, cfg SMTPConfig, to string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
	if err != nil {
		return err
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	return doSMTP(client, cfg, to, msg)
}

func sendMailSTARTTLS(addr string, cfg SMTPConfig, to string, msg []byte) error {
	client, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return err
		}
	}
	return doSMTP(client, cfg, to, msg)
}

func doSMTP(client *smtp.Client, cfg SMTPConfig, to string, msg []byte) error {
	if strings.TrimSpace(cfg.Username) != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(cfg.FromAddr); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
