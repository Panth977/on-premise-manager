package cloud

import (
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"opm/agent/internal/cloud/driver"
)

// SendMail is sendMail of routes/accounts.ts: OPM_SMTP_URL (smtp://user:pass@host:587, smtps://…:465); failures are logged.
func SendMail(to, subject, text string) {
	if Env.SmtpURL() == "" {
		return
	}
	if err := sendSMTP(Env.SmtpURL(), Env.SmtpFrom(), to, subject, text); err != nil {
		driver.Log.Warn("password mail failed", "to", to, "error", err.Error())
	}
}

// mailAuth is PLAIN, else LOGIN. Unlike smtp.PlainAuth it also works without TLS, as nodemailer does.
type mailAuth struct {
	plain      bool
	user, pass string
}

func (a *mailAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	if a.plain {
		return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
	}
	return "LOGIN", nil, nil
}

func (a *mailAuth) Next(from []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	if strings.HasPrefix(strings.ToLower(string(from)), "user") {
		return []byte(a.user), nil
	}
	return []byte(a.pass), nil
}

func sendSMTP(rawURL, from, to, subject, text string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	implicit := u.Scheme == "smtps" || u.Query().Get("secure") == "true" || u.Port() == "465"
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "587"
		if implicit {
			port = "465"
		}
	}
	envelope := from
	if a, err := mail.ParseAddress(from); err == nil {
		envelope = a.Address
	}
	var msg strings.Builder
	head := func(k, v string) { fmt.Fprintf(&msg, "%s: %s\r\n", k, v) }
	head("From", from)
	head("To", to)
	head("Subject", mime.QEncoding.Encode("utf-8", subject))
	head("Date", time.Now().UTC().Format(time.RFC1123Z))
	head("Message-ID", "<"+RandomUUID()+"@"+host+">")
	head("MIME-Version", "1.0")
	head("Content-Type", "text/plain; charset=utf-8")
	head("Content-Transfer-Encoding", "8bit")
	msg.WriteString("\r\n")
	msg.WriteString(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n"))
	addr := net.JoinHostPort(host, port)
	d := net.Dialer{Timeout: 30 * time.Second}
	var conn net.Conn
	if implicit {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: host})
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok && !implicit {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if u.User != nil {
		pw, _ := u.User.Password()
		if ok, mechs := c.Extension("AUTH"); ok {
			if err := c.Auth(&mailAuth{strings.Contains(strings.ToUpper(mechs), "PLAIN"), u.User.Username(), pw}); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(envelope); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg.String())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
