package notify

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"avxxx/internal/mailer"
	"avxxx/internal/webpush"
)

// Config is the notifier configuration read from the environment. A channel
// whose variables are all unset is disabled; a partially configured channel
// is an error (so a typo does not silently disable delivery).
//
//	VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY, VAPID_SUBJECT   Web Push
//	SMTP_HOST, SMTP_PORT (587), SMTP_USER, SMTP_PASSWORD, MAIL_FROM   email
//	BASE_URL              site origin for links (required for email)
//	NOTIFY_MAX_ATTEMPTS   attempts before a row is failed (6)
//	NOTIFY_BATCH, NOTIFY_CONCURRENCY, NOTIFY_POLL, NOTIFY_MAX_AGE, PUSH_TTL
type Config struct {
	Push    *webpush.Client
	Mail    *mailer.SMTP
	BaseURL string
	Opts    Options
}

// LoadConfig builds the configuration from getenv (os.Getenv).
func LoadConfig(getenv func(string) string) (Config, error) {
	var c Config
	var errs []error
	c.BaseURL = strings.TrimRight(strings.TrimSpace(getenv("BASE_URL")), "/")
	if c.BaseURL != "" {
		if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, errors.New("BASE_URL must be an absolute http(s) URL"))
		}
	}

	pub, priv, subj := getenv("VAPID_PUBLIC_KEY"), getenv("VAPID_PRIVATE_KEY"), getenv("VAPID_SUBJECT")
	switch {
	case pub == "" && priv == "" && subj == "":
	case pub == "" || priv == "" || subj == "":
		errs = append(errs, errors.New("VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY and VAPID_SUBJECT must be set together"))
	default:
		v, err := webpush.NewVAPID(pub, priv, subj)
		if err != nil {
			errs = append(errs, err) // never includes key material
			break
		}
		ttl, err := duration(getenv, "PUSH_TTL", 24*time.Hour)
		errs = append(errs, err)
		c.Push = &webpush.Client{VAPID: v, HTTP: &http.Client{Timeout: 30 * time.Second}, TTL: ttl, Urgency: "normal"}
	}

	host := getenv("SMTP_HOST")
	if host != "" || getenv("MAIL_FROM") != "" {
		port := 587
		if p := getenv("SMTP_PORT"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				errs = append(errs, errors.New("SMTP_PORT must be a number"))
			}
			port = n
		}
		if c.BaseURL == "" {
			errs = append(errs, errors.New("BASE_URL is required for email (links and List-Unsubscribe)"))
		}
		m, err := mailer.New(mailer.Config{Host: host, Port: port, User: getenv("SMTP_USER"),
			Password: getenv("SMTP_PASSWORD"), From: getenv("MAIL_FROM")})
		errs = append(errs, err)
		c.Mail = m
	}

	var err error
	if c.Opts.MaxAttempts, err = integer(getenv, "NOTIFY_MAX_ATTEMPTS"); err != nil {
		errs = append(errs, err)
	}
	if c.Opts.Batch, err = integer(getenv, "NOTIFY_BATCH"); err != nil {
		errs = append(errs, err)
	}
	if c.Opts.Concurrency, err = integer(getenv, "NOTIFY_CONCURRENCY"); err != nil {
		errs = append(errs, err)
	}
	if c.Opts.Poll, err = duration(getenv, "NOTIFY_POLL", 0); err != nil {
		errs = append(errs, err)
	}
	if c.Opts.MaxAge, err = duration(getenv, "NOTIFY_MAX_AGE", 0); err != nil {
		errs = append(errs, err)
	}
	return c, errors.Join(errs...)
}

func integer(getenv func(string) string, key string) (int, error) {
	v := getenv(key)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return n, nil
}

func duration(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def, fmt.Errorf("%s must be a positive duration like 30s or 24h", key)
	}
	return d, nil
}
