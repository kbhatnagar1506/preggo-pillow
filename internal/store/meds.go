package store

// Medication reminders.
//
// Two tables, for the same reason commands and detections are separate: what
// was *prescribed* and what was *taken* are different facts, and an adherence
// number is only meaningful when the second is recorded independently of the
// first. Changing a medication's schedule must not silently rewrite the
// history of what you actually swallowed last Tuesday.
//
// Doses are materialised lazily, for a date that has already arrived. There is
// no scheduler: a row appears the first time the day is looked at. That keeps
// the device stateless between restarts — a pillow that has been unplugged for
// a week must not wake up and claim you missed twenty-one doses it never
// asked you about.

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const medsSchema = `
CREATE TABLE IF NOT EXISTS medications (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT    NOT NULL,
  dose       TEXT    NOT NULL DEFAULT '',
  times      TEXT    NOT NULL DEFAULT '',
  created_ms INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS doses (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  med_id   INTEGER NOT NULL,
  due_date TEXT    NOT NULL,
  due_time TEXT    NOT NULL,
  taken_ms INTEGER,
  skipped  INTEGER NOT NULL DEFAULT 0,
  UNIQUE(med_id, due_date, due_time)
);
CREATE INDEX IF NOT EXISTS idx_doses_date ON doses(due_date);
`

// Medication is one thing to take, and when.
type Medication struct {
	ID    int64    `json:"id"`
	Name  string   `json:"name"`
	Dose  string   `json:"dose"`
	Times []string `json:"times"` // "HH:MM", 24-hour, sorted
}

// Dose is one scheduled occurrence on one day.
type Dose struct {
	MedID   int64  `json:"med_id"`
	Name    string `json:"name"`
	Dose    string `json:"dose"`
	Date    string `json:"date"`
	Time    string `json:"time"`
	TakenMs int64  `json:"taken_ms,omitempty"`
	Skipped bool   `json:"skipped"`
}

// NormaliseTimes accepts the sloppy input a form produces — "8:00, 20:30",
// stray spaces, a trailing comma — and returns sorted "HH:MM" values. An
// unparseable entry is an error rather than a silently dropped reminder.
func NormaliseTimes(raw string) ([]string, error) {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		t, err := time.Parse("15:04", part)
		if err != nil {
			if t, err = time.Parse("3:04PM", strings.ToUpper(strings.ReplaceAll(part, " ", ""))); err != nil {
				return nil, fmt.Errorf("%q is not a time like 08:00", part)
			}
		}
		out = append(out, t.Format("15:04"))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("give at least one time, like 08:00")
	}
	sort.Strings(out)
	// Two reminders at the same minute is one reminder.
	uniq := out[:1]
	for _, v := range out[1:] {
		if v != uniq[len(uniq)-1] {
			uniq = append(uniq, v)
		}
	}
	return uniq, nil
}

func (s *Store) AddMedication(name, dose string, times []string) (int64, error) {
	if err := s.ok(); err != nil {
		return 0, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("a medication needs a name")
	}
	if len(times) == 0 {
		return 0, fmt.Errorf("a medication needs at least one time")
	}
	res, err := s.db.Exec(`INSERT INTO medications(name, dose, times, created_ms) VALUES(?,?,?,?)`,
		name, strings.TrimSpace(dose), strings.Join(times, ","), time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("add medication: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) Medications() ([]Medication, error) {
	if err := s.ok(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, name, dose, times FROM medications ORDER BY times, name`)
	if err != nil {
		return nil, fmt.Errorf("list medications: %w", err)
	}
	defer rows.Close()
	out := []Medication{}
	for rows.Next() {
		var m Medication
		var times string
		if err := rows.Scan(&m.ID, &m.Name, &m.Dose, &times); err != nil {
			return nil, err
		}
		if times != "" {
			m.Times = strings.Split(times, ",")
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RemoveMedication drops the schedule but KEEPS the doses already recorded.
// Deleting the history along with the plan would quietly improve every
// adherence figure that follows, which is the one thing this number must
// never do.
func (s *Store) RemoveMedication(id int64) error {
	if err := s.ok(); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM medications WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("remove medication: %w", err)
	}
	return nil
}

// DosesOn returns the doses due on a local date, creating the rows the first
// time that day is asked for. Future dates are listed but not written: nothing
// is owed until the day arrives.
func (s *Store) DosesOn(date string, today string) ([]Dose, error) {
	if err := s.ok(); err != nil {
		return nil, err
	}
	meds, err := s.Medications()
	if err != nil {
		return nil, err
	}
	if date <= today {
		for _, m := range meds {
			for _, t := range m.Times {
				if _, err := s.db.Exec(
					`INSERT OR IGNORE INTO doses(med_id, due_date, due_time) VALUES(?,?,?)`,
					m.ID, date, t); err != nil {
					return nil, fmt.Errorf("materialise dose: %w", err)
				}
			}
		}
	}

	state := map[string]Dose{}
	rows, err := s.db.Query(
		`SELECT med_id, due_time, COALESCE(taken_ms,0), skipped FROM doses WHERE due_date = ?`, date)
	if err != nil {
		return nil, fmt.Errorf("read doses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d Dose
		var skipped int
		if err := rows.Scan(&d.MedID, &d.Time, &d.TakenMs, &skipped); err != nil {
			return nil, err
		}
		d.Skipped = skipped == 1
		state[fmt.Sprintf("%d@%s", d.MedID, d.Time)] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := []Dose{}
	for _, m := range meds {
		for _, t := range m.Times {
			d := state[fmt.Sprintf("%d@%s", m.ID, t)]
			d.MedID, d.Name, d.Dose, d.Date, d.Time = m.ID, m.Name, m.Dose, date, t
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time != out[j].Time {
			return out[i].Time < out[j].Time
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// MarkDose records what happened. state is "taken", "skipped" or "due";
// "due" undoes a mistap, which has to be possible or people stop being honest
// with the button.
func (s *Store) MarkDose(medID int64, date, at, state string) error {
	if err := s.ok(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO doses(med_id, due_date, due_time) VALUES(?,?,?)`,
		medID, date, at); err != nil {
		return fmt.Errorf("mark dose: %w", err)
	}
	var takenMs any
	skipped := 0
	switch state {
	case "taken":
		takenMs = time.Now().UnixMilli()
	case "skipped":
		skipped = 1
	case "due":
	default:
		return fmt.Errorf("unknown dose state %q", state)
	}
	_, err := s.db.Exec(`UPDATE doses SET taken_ms = ?, skipped = ? WHERE med_id = ? AND due_date = ? AND due_time = ?`,
		takenMs, skipped, medID, date, at)
	if err != nil {
		return fmt.Errorf("mark dose: %w", err)
	}
	return nil
}

// Adherence counts taken against due over the last n days ending today.
// Only days that have arrived count, so this morning's untaken pill does not
// read as a miss at 7am.
func (s *Store) Adherence(days int, today string) (taken, total int, err error) {
	if err := s.ok(); err != nil {
		return 0, 0, err
	}
	if days < 1 {
		days = 1
	}
	day, err := time.Parse("2006-01-02", today)
	if err != nil {
		return 0, 0, fmt.Errorf("adherence: %w", err)
	}
	from := day.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	row := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN taken_ms IS NOT NULL THEN 1 ELSE 0 END),0)
		   FROM doses WHERE due_date >= ? AND due_date <= ?`, from, today)
	if err := row.Scan(&total, &taken); err != nil {
		return 0, 0, fmt.Errorf("adherence: %w", err)
	}
	return taken, total, nil
}
