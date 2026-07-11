// Package ehr provides a thin SQLite-backed lookup layer for dummy patient
// records, keyed by the patient_id carried in SESSION_INIT.
package ehr

import (
	"database/sql"
	_ "embed"
	"errors"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed seed.sql
var seedSQL string

var ErrPatientNotFound = errors.New("ehr: patient not found")

type Patient struct {
	ID             string
	Name           string
	Age            int
	Sex            string
	Conditions     []string
	Allergies      []string
	Medications    []string
	LastVisitNotes string
}

type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite file at path and seeds it
// with dummy patients on first run.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(seedSQL); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) GetPatient(id string) (*Patient, error) {
	row := s.db.QueryRow(
		`SELECT id, name, age, sex, conditions, allergies, medications, last_visit_notes
		 FROM patients WHERE id = ?`, id)

	var p Patient
	var conditions, allergies, medications string
	if err := row.Scan(&p.ID, &p.Name, &p.Age, &p.Sex, &conditions, &allergies, &medications, &p.LastVisitNotes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPatientNotFound
		}
		return nil, err
	}
	p.Conditions = splitCSV(conditions)
	p.Allergies = splitCSV(allergies)
	p.Medications = splitCSV(medications)
	return &p, nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
