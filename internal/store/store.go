// Package store persists everything to SQLite.
//
// Three tables matter and they are deliberately separate:
//
//	commands   - the servo fired. GROUND TRUTH ONLY. Never shown as the count.
//	detections - the sensors decided a kick happened. THIS is the count.
//	presses    - a human pressed the button saying they felt one.
//
// Keeping commands and detections apart is what lets the system score itself
// live, and it is the answer to the question a judge will ask: "does the count
// come from the sensor, or from the thing that made the kick?"
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so it cross-compiles to the Pi
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS commands (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  t_ms     INTEGER NOT NULL,
  strength TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_commands_t ON commands(t_ms);

CREATE TABLE IF NOT EXISTS detections (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  t_ms       INTEGER NOT NULL,
  residual   REAL    NOT NULL,
  confidence REAL    NOT NULL,
  acoustic   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_detections_t ON detections(t_ms);

CREATE TABLE IF NOT EXISTS presses (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  t_ms   INTEGER NOT NULL,
  source TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_presses_t ON presses(t_ms);

CREATE TABLE IF NOT EXISTS nights (
  night_date TEXT PRIMARY KEY,
  kick_count INTEGER NOT NULL,
  simulated  INTEGER NOT NULL DEFAULT 0
);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func ms(t time.Time) int64 { return t.UnixMilli() }

func (s *Store) InsertCommand(t time.Time, strength string) error {
	_, err := s.db.Exec(`INSERT INTO commands(t_ms, strength) VALUES(?,?)`, ms(t), strength)
	return err
}

func (s *Store) InsertDetection(t time.Time, residual, confidence float64, acoustic bool) error {
	a := 0
	if acoustic {
		a = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO detections(t_ms, residual, confidence, acoustic) VALUES(?,?,?,?)`,
		ms(t), residual, confidence, a)
	return err
}

func (s *Store) InsertPress(t time.Time, source string) error {
	_, err := s.db.Exec(`INSERT INTO presses(t_ms, source) VALUES(?,?)`, ms(t), source)
	return err
}

// Score is the blind-test result: how the machine did, and how the human did,
// against the same set of commanded kicks.
type Score struct {
	Commands        int     `json:"commands"`
	MachineHits     int     `json:"machine_hits"`
	MachineMissed   int     `json:"machine_missed"`
	MachineFalsePos int     `json:"machine_false_pos"`
	HumanHits       int     `json:"human_hits"`
	HumanMissed     int     `json:"human_missed"`
	ToleranceMS     int64   `json:"tolerance_ms"`
	MachineRate     float64 `json:"machine_rate"`
	HumanRate       float64 `json:"human_rate"`
}

// ScoreWindow matches detections and button presses against commands inside a
// time window. A hit is anything landing within tolerance of a commanded kick.
//
// This is the query behind the reveal slide, and it is why the two tables are
// separate.
func (s *Store) ScoreWindow(from, to time.Time, tolerance time.Duration) (Score, error) {
	tol := tolerance.Milliseconds()
	sc := Score{ToleranceMS: tol}

	row := s.db.QueryRow(`SELECT COUNT(*) FROM commands WHERE t_ms BETWEEN ? AND ?`, ms(from), ms(to))
	if err := row.Scan(&sc.Commands); err != nil {
		return sc, err
	}

	// commands that had at least one detection within tolerance
	q := `
SELECT COUNT(*) FROM commands c
WHERE c.t_ms BETWEEN ? AND ?
  AND EXISTS (SELECT 1 FROM detections d
              WHERE d.t_ms BETWEEN c.t_ms - ? AND c.t_ms + ?)`
	if err := s.db.QueryRow(q, ms(from), ms(to), tol, tol).Scan(&sc.MachineHits); err != nil {
		return sc, err
	}

	// detections that matched no command at all
	q = `
SELECT COUNT(*) FROM detections d
WHERE d.t_ms BETWEEN ? AND ?
  AND NOT EXISTS (SELECT 1 FROM commands c
                  WHERE c.t_ms BETWEEN d.t_ms - ? AND d.t_ms + ?)`
	if err := s.db.QueryRow(q, ms(from), ms(to), tol, tol).Scan(&sc.MachineFalsePos); err != nil {
		return sc, err
	}

	// commands the human caught. Humans are slower than the detector, so the
	// tolerance is wider and asymmetric: a press before the kick is not a hit.
	humanTol := tol + 1200
	q = `
SELECT COUNT(*) FROM commands c
WHERE c.t_ms BETWEEN ? AND ?
  AND EXISTS (SELECT 1 FROM presses p
              WHERE p.t_ms BETWEEN c.t_ms - 200 AND c.t_ms + ?)`
	if err := s.db.QueryRow(q, ms(from), ms(to), humanTol).Scan(&sc.HumanHits); err != nil {
		return sc, err
	}

	sc.MachineMissed = sc.Commands - sc.MachineHits
	sc.HumanMissed = sc.Commands - sc.HumanHits
	if sc.Commands > 0 {
		sc.MachineRate = float64(sc.MachineHits) / float64(sc.Commands)
		sc.HumanRate = float64(sc.HumanHits) / float64(sc.Commands)
	}
	return sc, nil
}

// Event is a timestamped marker used to draw the reveal overlay.
type Event struct {
	TMS  int64  `json:"t_ms"`
	Kind string `json:"kind"` // command | detection | press
	Meta string `json:"meta"`
}

// EventsWindow returns everything that happened in a window, for the overlay.
func (s *Store) EventsWindow(from, to time.Time) ([]Event, error) {
	q := `
SELECT t_ms, 'command'   AS kind, strength AS meta FROM commands   WHERE t_ms BETWEEN ? AND ?
UNION ALL
SELECT t_ms, 'detection' AS kind, CAST(ROUND(confidence,2) AS TEXT) FROM detections WHERE t_ms BETWEEN ? AND ?
UNION ALL
SELECT t_ms, 'press'     AS kind, source   FROM presses    WHERE t_ms BETWEEN ? AND ?
ORDER BY t_ms`
	a, b := ms(from), ms(to)
	rows, err := s.db.Query(q, a, b, a, b, a, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.TMS, &e.Kind, &e.Meta); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Night is one night's aggregate, used for the baseline trend.
type Night struct {
	Date      string `json:"date"`
	KickCount int    `json:"kick_count"`
	Simulated bool   `json:"simulated"`
}

func (s *Store) UpsertNight(date string, count int, simulated bool) error {
	sim := 0
	if simulated {
		sim = 1
	}
	_, err := s.db.Exec(`
INSERT INTO nights(night_date, kick_count, simulated) VALUES(?,?,?)
ON CONFLICT(night_date) DO UPDATE SET kick_count=excluded.kick_count, simulated=excluded.simulated`,
		date, count, sim)
	return err
}

func (s *Store) Nights(limit int) ([]Night, error) {
	rows, err := s.db.Query(
		`SELECT night_date, kick_count, simulated FROM nights ORDER BY night_date DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Night
	for rows.Next() {
		var n Night
		var sim int
		if err := rows.Scan(&n.Date, &n.KickCount, &sim); err != nil {
			return nil, err
		}
		n.Simulated = sim == 1
		out = append(out, n)
	}
	// oldest first for charting
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// Baseline is the median of the prior nights, excluding the most recent.
// Median rather than mean, because one restless night should not move the bar.
func (s *Store) Baseline(excludeLast int) (float64, error) {
	nights, err := s.Nights(60)
	if err != nil {
		return 0, err
	}
	if len(nights) <= excludeLast {
		return 0, nil
	}
	vals := make([]int, 0, len(nights)-excludeLast)
	for _, n := range nights[:len(nights)-excludeLast] {
		vals = append(vals, n.KickCount)
	}
	if len(vals) == 0 {
		return 0, nil
	}
	// simple insertion sort, the list is tiny
	for i := 1; i < len(vals); i++ {
		for j := i; j > 0 && vals[j] < vals[j-1]; j-- {
			vals[j], vals[j-1] = vals[j-1], vals[j]
		}
	}
	mid := len(vals) / 2
	if len(vals)%2 == 1 {
		return float64(vals[mid]), nil
	}
	return float64(vals[mid-1]+vals[mid]) / 2, nil
}

// CountDetectionsOn returns how many detections landed on a calendar day,
// local time. Used by the nightly rollup so the trend reflects real sensor
// output rather than seeded data.
func (s *Store) CountDetectionsOn(day string) (int, error) {
	t, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		return 0, err
	}
	from := ms(t)
	to := ms(t.AddDate(0, 0, 1))
	var n int
	err = s.db.QueryRow(
		`SELECT COUNT(*) FROM detections WHERE t_ms >= ? AND t_ms < ?`, from, to).Scan(&n)
	return n, err
}
