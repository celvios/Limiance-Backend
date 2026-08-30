package observability

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// DatabaseCollector exposes operational state derived from authoritative
// database records. Collection is read-only and bounded so metrics scraping
// can never mutate or block the ledger processing path.
type DatabaseCollector struct {
	pool                *pgxpool.Pool
	activeSessions      *prometheus.Desc
	deposits            *prometheus.Desc
	withdrawals         *prometheus.Desc
	withdrawalBacklog   *prometheus.Desc
	outboxPublished     *prometheus.Desc
	outboxBacklog       *prometheus.Desc
	outboxLag           *prometheus.Desc
	ledgerImbalances    *prometheus.Desc
	lastWebhookUnixTime *prometheus.Desc
}

func NewDatabaseCollector(pool *pgxpool.Pool) *DatabaseCollector {
	return &DatabaseCollector{
		pool:                pool,
		activeSessions:      prometheus.NewDesc("limiance_active_sessions", "Currently active, unrevoked user sessions.", nil, nil),
		deposits:            prometheus.NewDesc("limiance_deposits_total", "Deposits observed by status.", []string{"status"}, nil),
		withdrawals:         prometheus.NewDesc("limiance_withdrawals_total", "Withdrawals created by status.", []string{"status"}, nil),
		withdrawalBacklog:   prometheus.NewDesc("limiance_withdrawal_queue_backlog", "Withdrawals awaiting approval or custody submission.", []string{"status"}, nil),
		outboxPublished:     prometheus.NewDesc("limiance_sqs_messages_published_total", "Durable outbox messages published to SQS.", nil, nil),
		outboxBacklog:       prometheus.NewDesc("limiance_sqs_queue_backlog", "Durable messages waiting to be published to SQS.", []string{"queue"}, nil),
		outboxLag:           prometheus.NewDesc("limiance_sqs_queue_lag_seconds", "Age of the oldest durable message waiting for SQS publication.", []string{"queue"}, nil),
		ledgerImbalances:    prometheus.NewDesc("limiance_ledger_imbalanced_groups", "Count of posted journal/asset groups whose credits and debits differ.", nil, nil),
		lastWebhookUnixTime: prometheus.NewDesc("limiance_webhook_last_received_unixtime", "Unix timestamp of the most recent durable webhook receipt.", []string{"provider"}, nil),
	}
}

func (c *DatabaseCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{c.activeSessions, c.deposits, c.withdrawals, c.withdrawalBacklog, c.outboxPublished, c.outboxBacklog, c.outboxLag, c.ledgerImbalances, c.lastWebhookUnixTime} {
		ch <- desc
	}
}

func (c *DatabaseCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var count float64
	if c.pool.QueryRow(ctx, `SELECT count(*)::float8 FROM sessions WHERE revoked_at IS NULL AND expires_at > now()`).Scan(&count) == nil {
		ch <- prometheus.MustNewConstMetric(c.activeSessions, prometheus.GaugeValue, count)
	}
	c.collectStatusCounts(ctx, ch, `SELECT status,count(*)::float8 FROM deposits GROUP BY status`, c.deposits)
	c.collectStatusCounts(ctx, ch, `SELECT status,count(*)::float8 FROM withdrawals GROUP BY status`, c.withdrawals)
	c.collectStatusCounts(ctx, ch, `SELECT status,count(*)::float8 FROM withdrawals WHERE status IN ('pending_approval','approved') GROUP BY status`, c.withdrawalBacklog)

	var backlog, lag float64
	if c.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE published_at IS NOT NULL)::float8,count(*) FILTER (WHERE published_at IS NULL)::float8,COALESCE(EXTRACT(EPOCH FROM now()-min(occurred_at) FILTER (WHERE published_at IS NULL)),0)::float8 FROM outbox_events`).Scan(&count, &backlog, &lag) == nil {
		ch <- prometheus.MustNewConstMetric(c.outboxPublished, prometheus.CounterValue, count)
		ch <- prometheus.MustNewConstMetric(c.outboxBacklog, prometheus.GaugeValue, backlog, "outbox")
		ch <- prometheus.MustNewConstMetric(c.outboxLag, prometheus.GaugeValue, lag, "outbox")
	}
	if c.pool.QueryRow(ctx, `SELECT count(*)::float8 FROM (SELECT p.journal_id,p.asset_id FROM postings p JOIN journals j ON j.id=p.journal_id AND j.status='posted' GROUP BY p.journal_id,p.asset_id HAVING sum(CASE p.direction WHEN 'credit' THEN p.amount_atomic ELSE -p.amount_atomic END) <> 0) broken`).Scan(&count) == nil {
		ch <- prometheus.MustNewConstMetric(c.ledgerImbalances, prometheus.GaugeValue, count)
	}
	rows, err := c.pool.Query(ctx, `SELECT provider,EXTRACT(EPOCH FROM max(received_at))::float8 FROM webhook_receipts GROUP BY provider`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var provider string
			var timestamp float64
			if rows.Scan(&provider, &timestamp) == nil {
				ch <- prometheus.MustNewConstMetric(c.lastWebhookUnixTime, prometheus.GaugeValue, timestamp, provider)
			}
		}
	}
}

func (c *DatabaseCollector) collectStatusCounts(ctx context.Context, ch chan<- prometheus.Metric, query string, desc *prometheus.Desc) {
	rows, err := c.pool.Query(ctx, query)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count float64
		if rows.Scan(&status, &count) == nil {
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, count, status)
		}
	}
}
