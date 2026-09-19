// Package tiger is the longitudinal store, on TigerData (Timescale Cloud).
//
// WHY TWO DATABASES
// -----------------
// SQLite on the device is authoritative for the live session and never touches
// the network, because nothing in a live demo may depend on venue WiFi. Tiger
// holds the long record: 100 Hz across three sensors, eight hours a night, for
// the twelve weeks of a third trimester. That is a time-series problem, and
// hypertables plus continuous aggregates are the right tool for it rather than
// a nice-to-have.
//
// The nightly baseline that the whole product rests on IS a continuous
// aggregate. We do not compute it in application code here; Timescale
// maintains it incrementally.
//
// NIGHTS ARE NOT CALENDAR DAYS
// ----------------------------
// Sleep crosses midnight. Bucketing detections by calendar day splits one
// night's sleep into two rows and halves both, which would make a normal night
// look like two reduced ones and fire a false alert. The bucket origin is
// midday, so a "night" runs noon to noon and one sleep lands in one bucket.
package tiger

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the Tiger connection. A nil Store is safe to call: every method
// degrades to a no-op, because Lull must run with no network at all.
type Store struct {
	pool   *pgxpool.Pool
	device string
	logger *log.Logger
}

type Options struct {
	URL    string // falls back to TIGER_DATABASE_URL
	Device string // which physical unit this is; lets one database hold many
	Logger *log.Logger
}

func (s *Store) Enabled() bool { return s != nil && s.pool != nil }

// Open connects and migrates. It returns a disabled store and a nil error when
// no URL is configured, because running without Tiger is normal, not a failure.
func Open(ctx context.Context, o Options) (*Store, error) {
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	if o.URL == "" {
		o.URL = os.Getenv("TIGER_DATABASE_URL")
	}
	if o.URL == "" {
		o.Logger.Println("tiger: no TIGER_DATABASE_URL, longitudinal store is off (Lull works normally)")
		return &Store{logger: o.Logger}, nil
	}
	if o.Device == "" {
		o.Device = "lull-01"
	}

	cfg, err := pgxpool.ParseConfig(o.URL)
	if err != nil {
		return &Store{logger: o.Logger}, fmt.Errorf("tiger: bad url: %w", err)
	}
	// Small pool on purpose: this runs on a Raspberry Pi, and the connection
	// pooler is on the cloud side.
	cfg.MaxConns = 4
	cfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		o.Logger.Printf("tiger: connect failed (%v), longitudinal store is off", err)
		return &Store{logger: o.Logger}, nil
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		o.Logger.Printf("tiger: ping failed (%v), longitudinal store is off", err)
		return &Store{logger: o.Logger}, nil
	}

	s := &Store{pool: pool, device: o.Device, logger: o.Logger}
	if err := s.migrate(ctx); err != nil {
		o.Logger.Printf("tiger: migrate failed (%v), longitudinal store is off", err)
		pool.Close()
		return &Store{logger: o.Logger}, nil
	}
	o.Logger.Printf("tiger: connected, device=%s", o.Device)
	return s, nil
}

func (s *Store) Close() {
	if s.Enabled() {
		s.pool.Close()
	}
}

// migrate is idempotent so it can run on every boot.
func migrationSQL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS lull_detections (
			t          timestamptz      NOT NULL,
			device     text             NOT NULL,
			residual   double precision,
			confidence double precision,
			acoustic   boolean
		)`,
		`SELECT create_hypertable('lull_detections','t',if_not_exists=>TRUE)`,

		`CREATE TABLE IF NOT EXISTS lull_commands (
			t        timestamptz NOT NULL,
			device   text        NOT NULL,
			strength text
		)`,
		`SELECT create_hypertable('lull_commands','t',if_not_exists=>TRUE)`,

		`CREATE TABLE IF NOT EXISTS lull_presses (
			t      timestamptz NOT NULL,
			device text        NOT NULL,
			source text
		)`,
		`SELECT create_hypertable('lull_presses','t',if_not_exists=>TRUE)`,

		`CREATE TABLE IF NOT EXISTS lull_maternal (
			t               timestamptz NOT NULL,
			device          text        NOT NULL,
			posture         text,
			supine_minutes  double precision,
			respiration_rpm double precision,
			snore_percent   double precision,
			wake_events     int
		)`,
		`SELECT create_hypertable('lull_maternal','t',if_not_exists=>TRUE)`,

		// A night runs noon to noon. See the package comment: bucketing by
		// calendar day would split one sleep across two rows.
		`CREATE MATERIALIZED VIEW IF NOT EXISTS lull_nightly
		 WITH (timescaledb.continuous) AS
		 SELECT device,
		        time_bucket(INTERVAL '1 day', t, origin => TIMESTAMPTZ '2000-01-01 12:00:00+00') AS night,
		        count(*) AS movements
		 FROM lull_detections
		 GROUP BY device, night
		 WITH NO DATA`,
	}
}

// MigrationStatements is the schema, exported so it can be asserted on without
// a live database. The design decisions encoded in this SQL — noon-to-noon
// buckets above all — are invisible at a glance and easy to erase with a
// well-meaning edit.
func MigrationStatements() []string { return migrationSQL() }

func (s *Store) migrate(ctx context.Context) error {
	stmts := migrationSQL()

	for _, q := range stmts {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			// create_hypertable and the view are not re-runnable in every
			// Timescale version; "already exists" is success.
			if isAlreadyExists(err) {
				continue
			}
			return fmt.Errorf("%.60s...: %w", strings.ReplaceAll(q, "\n", " "), err)
		}
	}

	// Refresh policy is separate: it errors if one already exists, which is fine.
	_, err := s.pool.Exec(ctx, `
		SELECT add_continuous_aggregate_policy('lull_nightly',
			start_offset      => INTERVAL '30 days',
			end_offset        => INTERVAL '1 hour',
			schedule_interval => INTERVAL '1 hour',
			if_not_exists     => TRUE)`)
	if err != nil && !isAlreadyExists(err) {
		s.logger.Printf("tiger: continuous aggregate policy: %v (refreshing manually instead)", err)
	}
	return nil
}

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	e := strings.ToLower(err.Error())
	return strings.Contains(e, "already exists") ||
		strings.Contains(e, "already a hypertable") ||
		strings.Contains(e, "duplicate")
}

// ------------------------------------------------------------------ writes

// Detection is one row to sync.
type Detection struct {
	T          time.Time
	Residual   float64
	Confidence float64
	Acoustic   bool
}

// SyncDetections bulk-inserts. Called with whatever SQLite has not sent yet, so
// a period offline costs nothing but a later catch-up.
func (s *Store) SyncDetections(ctx context.Context, rows []Detection) error {
	if !s.Enabled() || len(rows) == 0 {
		return nil
	}
	batch := make([][]any, 0, len(rows))
	for _, r := range rows {
		batch = append(batch, []any{r.T, s.device, r.Residual, r.Confidence, r.Acoustic})
	}
	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"lull_detections"},
		[]string{"t", "device", "residual", "confidence", "acoustic"},
		pgx.CopyFromRows(batch))
	return err
}

// RecordMaternal stores one night's maternal summary.
func (s *Store) RecordMaternal(ctx context.Context, t time.Time,
	posture string, supineMin, respRPM, snorePct float64, wakes int) error {
	if !s.Enabled() {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO lull_maternal
		  (t, device, posture, supine_minutes, respiration_rpm, snore_percent, wake_events)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		t, s.device, posture, supineMin, respRPM, snorePct, wakes)
	return err
}

// ------------------------------------------------------------------ reads

// Night is one row of the continuous aggregate.
type Night struct {
	Night     time.Time
	Movements int
}

// Nights reads the continuous aggregate rather than counting rows. This is the
// point of using Timescale: the rollup is maintained incrementally instead of
// being recomputed by the application on a ticker.
func (s *Store) Nights(ctx context.Context, limit int) ([]Night, error) {
	if !s.Enabled() {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT night, movements FROM lull_nightly
		WHERE device = $1 ORDER BY night DESC LIMIT $2`, s.device, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Night
	for rows.Next() {
		var n Night
		if err := rows.Scan(&n.Night, &n.Movements); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// Baseline is the median of prior nights, excluding the most recent.
//
// Median rather than mean, because one restless night must not move the bar
// that an alert is measured against.
func (s *Store) Baseline(ctx context.Context) (float64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	var v *float64
	err := s.pool.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY movements)
		FROM lull_nightly
		WHERE device = $1
		  AND night < (SELECT max(night) FROM lull_nightly WHERE device = $1)`,
		s.device).Scan(&v)
	if err != nil || v == nil {
		return 0, err
	}
	return *v, nil
}

// Refresh materialises the aggregate now. The hourly policy is the steady
// state; this exists so a demo does not have to wait an hour to see its own
// data appear.
func (s *Store) Refresh(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	_, err := s.pool.Exec(ctx, `CALL refresh_continuous_aggregate('lull_nightly', NULL, NULL)`)
	return err
}
