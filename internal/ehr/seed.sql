CREATE TABLE IF NOT EXISTS patients (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    age              INTEGER NOT NULL,
    sex              TEXT NOT NULL,
    conditions       TEXT NOT NULL DEFAULT '',
    allergies        TEXT NOT NULL DEFAULT '',
    medications      TEXT NOT NULL DEFAULT '',
    last_visit_notes TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS doctors (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    username         TEXT NOT NULL UNIQUE,
    password_hash    TEXT NOT NULL
);

INSERT OR IGNORE INTO patients (id, name, age, sex, conditions, allergies, medications, last_visit_notes) VALUES
    ('1001', 'Amara Okafor',   34, 'F', 'Type 2 diabetes',            'Penicillin',      'Metformin 500mg',        'Stable glucose control, review in 3 months.'),
    ('1002', 'Rahul Sharma',   58, 'M', 'Hypertension, CAD',          'None known',      'Atorvastatin, Lisinopril', 'BP trending high, advised salt reduction.'),
    ('1003', 'Grace Mwangi',   27, 'F', 'Asthma',                     'Latex',           'Albuterol inhaler',      'Mild exacerbation last month, resolved.'),
    ('1004', 'Tomás Ferreira', 65, 'M', 'COPD, Osteoarthritis',       'Sulfa drugs',     'Tiotropium, Ibuprofen',  'Oxygen sat borderline, monitor closely in field.'),
    ('1005', 'Priya Natarajan', 41, 'F', 'None known',                'Shellfish',       'None',                    'Routine checkup, no active issues.');

-- Seed the default admin doctor (password: 'password')
-- SHA256 of 'password' is '5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8'
INSERT OR IGNORE INTO doctors (id, username, password_hash) VALUES
    (1, 'admin', '5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8');
