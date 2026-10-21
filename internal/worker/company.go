package worker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/store"
)

var (
	switchRe = regexp.MustCompile(`(?i)^(?:firma|şirket)(?:y[ıi])?\s*(?:değiştir|degistir|seç|sec)?\s+(.+)$`)
	listRe   = regexp.MustCompile(`(?i)^(?:firmalar(?:ım)?|şirketler(?:im)?|firma\s+değiştir|firma\s+seç|companies)$`)
)

// routeCentral picks the company for a message on a central connection and
// handles "firmalar" / "firma değiştir X" for users in several companies.
// handled=true means a reply was queued and the message must not reach the engine.
func (w *inboundWorker) routeCentral(ctx context.Context, msg domain.InboundMessage) (company domain.CompanyID, handled bool, err error) {
	if w.d.Directory == nil {
		return "", false, nil
	}
	ids, err := w.d.Directory.Lookup(ctx, msg.Channel, msg.Sender)
	if err != nil || len(ids) == 0 {
		return "", false, err
	}
	active, err := w.d.Store.ActiveCompany(ctx, msg.Channel, msg.Sender)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", false, err
	}
	text := strings.TrimSpace(msg.Text)
	if len(ids) > 1 && msg.Kind == domain.KindText {
		if listRe.MatchString(text) {
			return "", true, w.reply(ctx, msg, w.companyList(ctx, ids, active))
		}
		if m := switchRe.FindStringSubmatch(text); m != nil {
			if pick, ok := w.pickCompany(ctx, ids, m[1]); ok {
				if err := w.d.Store.SetActiveCompany(ctx, msg.Channel, msg.Sender, pick); err != nil {
					return "", false, err
				}
				return "", true, w.reply(ctx, msg, "✅ Aktif firma: "+w.companyName(ctx, pick))
			}
			return "", true, w.reply(ctx, msg, "Bu isimde bir firmanız yok.\n"+w.companyList(ctx, ids, active))
		}
	}
	for _, id := range ids {
		if id == active {
			return id, false, nil
		}
	}
	return ids[0], false, nil
}

func (w *inboundWorker) companyName(ctx context.Context, c domain.CompanyID) string {
	if snap, err := w.d.Tenants.Get(ctx, c); err == nil && snap.Settings.CompanyName != "" {
		return snap.Settings.CompanyName
	}
	return string(c)
}

func (w *inboundWorker) companyList(ctx context.Context, ids []domain.CompanyID, active domain.CompanyID) string {
	var b strings.Builder
	b.WriteString("Firmalarınız:")
	for i, id := range ids {
		mark := ""
		if id == active || (active == "" && i == 0) {
			mark = " (aktif)"
		}
		fmt.Fprintf(&b, "\n%d) %s%s", i+1, w.companyName(ctx, id), mark)
	}
	b.WriteString("\nDeğiştirmek için: \"firma değiştir 2\" veya \"firma değiştir <ad>\"")
	return b.String()
}

func (w *inboundWorker) pickCompany(ctx context.Context, ids []domain.CompanyID, q string) (domain.CompanyID, bool) {
	q = strings.TrimSpace(q)
	if n, err := strconv.Atoi(q); err == nil && n >= 1 && n <= len(ids) {
		return ids[n-1], true
	}
	fq := fake.Fold(q)
	for _, id := range ids {
		if fake.Fold(string(id)) == fq || strings.Contains(fake.Fold(w.companyName(ctx, id)), fq) {
			return id, true
		}
	}
	return "", false
}

func (w *inboundWorker) reply(ctx context.Context, msg domain.InboundMessage, text string) error {
	return w.d.Queue.EnqueueMany(ctx, []river.JobArgs{queue.OutboundArgs{Out: domain.OutboundMessage{
		CompanyID: msg.CompanyID, Channel: msg.Channel, ChannelID: msg.ChannelID, To: msg.Sender, Text: text,
	}}})
}
