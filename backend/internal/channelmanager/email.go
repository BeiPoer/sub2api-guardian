package channelmanager

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"sub2api-guardian/backend/internal/store"
)

func (m *Manager) EmailSettings() (store.UpstreamEmailSettings, error) {
	return m.store.UpstreamEmailSettings()
}

func (m *Manager) SaveEmailSettings(settings store.UpstreamEmailSettings) (store.UpstreamEmailSettings, error) {
	if err := validateEmailSettings(settings, false); err != nil {
		return store.UpstreamEmailSettings{}, err
	}
	return m.store.SaveUpstreamEmailSettings(settings)
}

func (m *Manager) TestEmail(ctx context.Context, recipients []string) (string, error) {
	return m.sendEmail(ctx, recipients, "SMTP 测试", "这是一封来自 Sub2API Guardian 上游渠道管理的测试邮件。")
}

func validateEmailSettings(settings store.UpstreamEmailSettings, requireComplete bool) error {
	if settings.SMTPPort <= 0 || settings.SMTPPort > 65535 {
		return invalid("SMTP 端口无效")
	}
	for _, header := range []string{settings.SMTPFrom, settings.SubjectPrefix} {
		if strings.ContainsAny(header, "\r\n") {
			return invalid("邮件头不能包含换行符")
		}
	}
	if settings.SMTPFrom != "" {
		if _, err := mail.ParseAddress(settings.SMTPFrom); err != nil {
			return invalid("SMTP 发件人格式无效")
		}
	}
	for _, recipient := range settings.DefaultRecipients {
		if _, err := parseRecipient(recipient); err != nil {
			return err
		}
	}
	if requireComplete && (strings.TrimSpace(settings.SMTPHost) == "" || settings.SMTPFrom == "") {
		return invalid("SMTP 主机和发件人未配置")
	}
	return nil
}

func parseRecipient(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", invalid("收件人不能包含换行符")
	}
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil || address.Address == "" {
		return "", invalid("收件人邮箱格式无效：" + value)
	}
	return address.Address, nil
}

func (m *Manager) sendEmail(ctx context.Context, recipients []string, subject, body string) (string, error) {
	settings, err := m.store.UpstreamEmailSettings()
	if err != nil {
		return "", err
	}
	if err := validateEmailSettings(settings, true); err != nil {
		return "", err
	}
	if len(recipients) == 0 {
		recipients = settings.DefaultRecipients
	}
	to := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		address, err := parseRecipient(recipient)
		if err != nil {
			return "", err
		}
		to = append(to, address)
	}
	if len(to) == 0 {
		return "", invalid("收件人未配置")
	}
	if strings.ContainsAny(subject, "\r\n") {
		return "", invalid("邮件主题不能包含换行符")
	}
	from, _ := mail.ParseAddress(settings.SMTPFrom)
	messageID, err := randomMessageID(from.Address)
	if err != nil {
		return "", err
	}
	message := buildMessage(settings.SMTPFrom, to, settings.SubjectPrefix+subject, body, messageID)
	if err := sendSMTP(ctx, settings, from.Address, to, message); err != nil {
		return "", err
	}
	return messageID, nil
}

func randomMessageID(address string) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	domain := "localhost"
	if _, host, found := strings.Cut(address, "@"); found && host != "" {
		domain = host
	}
	return "<" + hex.EncodeToString(random) + "@" + domain + ">", nil
}

func buildMessage(from string, to []string, subject, body, messageID string) []byte {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	headers := []string{
		"From: " + from,
		"To: " + strings.Join(to, ", "),
		"Subject: " + subject,
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: " + messageID,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
	}
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body + "\r\n")
}

func sendSMTP(ctx context.Context, settings store.UpstreamEmailSettings, from string, to []string, message []byte) error {
	address := net.JoinHostPort(settings.SMTPHost, strconv.Itoa(settings.SMTPPort))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var (
		client *smtp.Client
		conn   net.Conn
		err    error
	)
	tlsConfig := &tls.Config{ServerName: settings.SMTPHost, MinVersion: tls.VersionTLS12}
	if settings.SMTPSecure {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
		if err == nil {
			client, err = smtp.NewClient(conn, settings.SMTPHost)
		}
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			client, err = smtp.NewClient(conn, settings.SMTPHost)
		}
		if err == nil {
			if ok, _ := client.Extension("STARTTLS"); ok {
				err = client.StartTLS(tlsConfig)
			}
		}
	}
	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return fmt.Errorf("连接 SMTP 失败: %w", err)
	}
	defer func() { _ = client.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	if settings.SMTPUser != "" {
		auth := smtp.PlainAuth("", settings.SMTPUser, settings.SMTPPassword, settings.SMTPHost)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("SMTP 发件人被拒绝: %w", err)
	}
	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("SMTP 收件人被拒绝: %w", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA 失败: %w", err)
	}
	buffered := bufio.NewWriter(writer)
	if _, err := buffered.Write(message); err != nil {
		_ = writer.Close()
		return err
	}
	if err := buffered.Flush(); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := client.Quit(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
