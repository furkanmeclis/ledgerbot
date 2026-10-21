package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

func init() {
	// Decode ISO-8859-9 / Windows-1254 bodies common in Turkish mail.
	message.CharsetReader = charset.Reader
}

// Poll reads unseen mail over IMAP (TLS) when imap_host is configured. Messages
// are marked \Seen only after they were accepted by the queue, so a crash
// re-delivers instead of losing mail. Without IMAP settings it blocks until
// ctx ends (inbound mail then arrives through the webhook).
func (Email) Poll(ctx context.Context, c Conn, sink func(context.Context, domain.InboundMessage) error) error {
	var cfg EmailConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	if cfg.IMAPHost == "" {
		<-ctx.Done()
		return nil
	}
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		err := pollOnce(ctx, c, cfg, sink)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			backoff = min(backoff*2, 5*time.Minute)
		} else {
			backoff = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
	return nil
}

func pollOnce(ctx context.Context, c Conn, cfg EmailConfig, sink func(context.Context, domain.InboundMessage) error) error {
	host := cfg.IMAPHost
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "993")
	}
	cl, err := client.DialTLS(host, nil)
	if err != nil {
		return err
	}
	defer cl.Logout() //nolint:errcheck
	cl.Timeout = 60 * time.Second
	user := cfg.IMAPUser
	if user == "" {
		user = cfg.Address
	}
	if err := cl.Login(user, cfg.IMAPPass); err != nil {
		return err
	}
	if _, err := cl.Select("INBOX", false); err != nil {
		return err
	}
	crit := imap.NewSearchCriteria()
	crit.WithoutFlags = []string{imap.SeenFlag}
	uids, err := cl.UidSearch(crit)
	if err != nil || len(uids) == 0 {
		return err
	}
	if len(uids) > 50 {
		uids = uids[:50] // bounded batches; the rest comes on the next poll
	}
	set := new(imap.SeqSet)
	set.AddNum(uids...)
	section := &imap.BodySectionName{Peek: true}
	msgs := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() { done <- cl.UidFetch(set, []imap.FetchItem{section.FetchItem(), imap.FetchUid}, msgs) }()
	var seen []uint32
	for m := range msgs {
		if ctx.Err() != nil {
			continue // drain
		}
		body := m.GetBody(section)
		if body == nil {
			continue
		}
		in, err := parseMail(c, m.Uid, body)
		if err != nil {
			seen = append(seen, m.Uid) // unparseable: do not retry forever
			continue
		}
		ok := true
		for _, msg := range in {
			if err := sink(ctx, msg); err != nil {
				ok = false
				break
			}
		}
		if ok {
			seen = append(seen, m.Uid)
		}
	}
	if err := <-done; err != nil {
		return err
	}
	if len(seen) > 0 {
		mark := new(imap.SeqSet)
		mark.AddNum(seen...)
		return cl.UidStore(mark, imap.FormatFlagsOp(imap.AddFlags, true), []any{imap.SeenFlag}, nil)
	}
	return nil
}

func parseMail(c Conn, uid uint32, r io.Reader) ([]domain.InboundMessage, error) {
	mr, err := gomail.CreateReader(r)
	if err != nil {
		return nil, err
	}
	from, subject := "", ""
	if list, err := mr.Header.AddressList("From"); err == nil && len(list) > 0 {
		from = list[0].String()
	}
	subject, _ = mr.Header.Subject()
	id, _ := mr.Header.MessageID()
	if id == "" {
		id = "uid" + strconv.FormatUint(uint64(uid), 10)
	}
	var body strings.Builder
	var atts []attachment
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch h := p.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := h.ContentType()
			if ct == "text/plain" && body.Len() == 0 {
				b, _ := io.ReadAll(io.LimitReader(p.Body, 1<<20))
				body.Write(b)
			}
		case *gomail.AttachmentHeader:
			name, _ := h.Filename()
			ct, _, _ := h.ContentType()
			b, err := io.ReadAll(io.LimitReader(p.Body, MaxBody))
			if err == nil && len(b) > 0 {
				atts = append(atts, attachment{name: name, mime: ct, data: b})
			}
		}
	}
	return mailToInbound(c, id, from, subject, body.String(), atts), nil
}
