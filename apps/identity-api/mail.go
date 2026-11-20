package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
)

type mailSender interface {
	send(context.Context, string, string, string) error
}

type smtpMailer struct {
	addr     string
	from     string
	username string
	password string
	mode     string
}

func (m smtpMailer) send(ctx context.Context, to, subject, body string) error {
	host, err := m.host()
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if m.mode == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: &dialer, Config: tlsConfig}).DialContext(ctx, "tcp", m.addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", m.addr)
	}
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	if m.mode == "starttls" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if m.username != "" {
		if err := client.Auth(smtp.PlainAuth("", m.username, m.password, host)); err != nil {
			return err
		}
	}
	if err := client.Mail(m.from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	data, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := data.Write(mailMessage(m.from, to, subject, body)); err != nil {
		return err
	}
	if err := data.Close(); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}

func mailMessage(from, to, subject, body string) []byte {
	var message bytes.Buffer
	_, _ = fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n",
		from, to, mime.QEncoding.Encode("UTF-8", subject), time.Now().UTC().Format(time.RFC1123Z))
	writer := quotedprintable.NewWriter(&message)
	_, _ = writer.Write([]byte(body))
	_ = writer.Close()
	return message.Bytes()
}

type mailJob struct {
	id       int64
	purpose  string
	email    string
	attempts int
}

func (s store) nextMail(ctx context.Context) (mailJob, error) {
	var job mailJob
	err := s.pool.QueryRow(ctx, `
		WITH pending AS (
			SELECT id FROM identity_mail_requests WHERE next_at <= now() AND attempts < 5
			ORDER BY next_at, id FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE identity_mail_requests AS requests
		SET attempts = requests.attempts + 1, next_at = now() + interval '1 minute'
		FROM pending WHERE requests.id = pending.id
		RETURNING requests.id, requests.purpose, requests.email, requests.attempts
	`).Scan(&job.id, &job.purpose, &job.email, &job.attempts)
	return job, err
}

func (s store) finishMail(ctx context.Context, job mailJob) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM identity_mail_requests WHERE id = $1 AND attempts = $2", job.id, job.attempts)
	return err
}

func (s store) prune(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM identity_challenges
		WHERE expires_at <= now() AND (legacy_expires_at IS NULL OR legacy_expires_at <= now());
		DELETE FROM identity_sessions WHERE expires_at <= now();
		DELETE FROM identity_rate_limits WHERE expires_at <= now();
		DELETE FROM identity_mail_requests WHERE next_at <= now() AND (attempts >= 5 OR created_at <= now() - interval '1 day');
	`)
	return err
}

func runMailer(ctx context.Context, s store, sender mailSender, publicURL *url.URL, mailTokenKey [32]byte) {
	var pruneAt time.Time
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if time.Now().After(pruneAt) {
			pruneContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := s.prune(pruneContext); err != nil && ctx.Err() == nil {
				slog.Warn("clean up expired identity data")
			}
			cancel()
			pruneAt = time.Now().Add(time.Hour)
		}
		for {
			jobContext, cancel := context.WithTimeout(ctx, 20*time.Second)
			job, err := s.nextMail(jobContext)
			if err == nil {
				err = processMail(jobContext, s, sender, publicURL, mailTokenKey, job)
			}
			cancel()
			if err != nil {
				if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
					slog.Warn("deliver identity email", "job", job.id, "attempt", job.attempts)
				}
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func processMail(ctx context.Context, s store, sender mailSender, publicURL *url.URL, mailTokenKey [32]byte, job mailJob) error {
	token, err := s.mailChallenge(ctx, mailTokenKey, job)
	if err != nil {
		return err
	}
	if token != "" {
		subject, body := challengeMessage(publicURL, job.purpose, token)
		if err := sender.send(ctx, job.email, subject, body); err != nil {
			if job.attempts == 5 {
				if finishErr := s.finishMail(ctx, job); finishErr != nil {
					return finishErr
				}
			}
			return err
		}
	}
	return s.finishMail(ctx, job)
}

func challengeMessage(publicURL *url.URL, purpose, token string) (string, string) {
	link := publicURL.ResolveReference(&url.URL{Path: "/" + purpose})
	query := url.Values{"token": {token}}
	link.RawQuery = query.Encode()
	subject := "Подтвердите почту для KadrRoom"
	instructions := "Перейдите по ссылке и задайте пароль. Срок действия ссылки с первого письма — 24 часа."
	if purpose == purposeReset {
		subject = "Восстановление пароля KadrRoom"
		instructions = "Перейдите по ссылке и задайте новый пароль. Срок действия ссылки с первого письма — 30 минут."
	}
	return subject, instructions + "\r\n\r\n" + link.String() +
		"\r\n\r\nЕсли вы ничего не запрашивали, проигнорируйте это письмо.\r\n"
}
