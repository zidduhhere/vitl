// Package ehr provides a thin SQLite-backed lookup layer for dummy patient
// records, keyed by the patient_id carried in SESSION_INIT.
package ehr

import (
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
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

// AuthenticateDoctor checks if a doctor exists with the given username and password.
// It computes the SHA256 of the password and compares it against the stored hash.
func (s *Store) AuthenticateDoctor(username, password string) (int, error) {
	// Compute SHA256 hash of the password
	hash := sha256.Sum256([]byte(password))
	hashHex := hex.EncodeToString(hash[:])

	var id int
	var storedHash string

	err := s.db.QueryRow(`SELECT id, password_hash FROM doctors WHERE username = ?`, username).Scan(&id, &storedHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("invalid username or password")
		}
		return 0, err
	}

	if storedHash != hashHex {
		return 0, errors.New("invalid username or password")
	}

	return id, nil
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

func (s *Store) AddPatient(p *Patient) (string, error) {
	var maxID sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(CAST(id AS INTEGER)) FROM patients`).Scan(&maxID)
	if err != nil {
		return "", err
	}

	nextID := int64(1001)
	if maxID.Valid {
		nextID = maxID.Int64 + 1
	}

	p.ID = fmt.Sprintf("%d", nextID)

	_, err = s.db.Exec(`
		INSERT INTO patients (id, name, age, sex, conditions, allergies, medications, last_visit_notes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.Name, p.Age, p.Sex, joinCSV(p.Conditions), joinCSV(p.Allergies), joinCSV(p.Medications), p.LastVisitNotes)

	return p.ID, err
}

func joinCSV(parts []string) string {
	return strings.Join(parts, ", ")
}
