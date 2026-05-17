package model

import "time"

type Scan struct {
	ID            string    `json:"id"`
	PlatformID    string    `json:"platform_id"`
	Scanner       string    `json:"scanner"`
	DBVersion     string    `json:"db_version"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`
	VulnCount     int       `json:"vuln_count"`
	CriticalCount int       `json:"critical_count"`
	HighCount     int       `json:"high_count"`
	MediumCount   int       `json:"medium_count"`
	LowCount      int       `json:"low_count"`
	Status        string    `json:"status"`
	ErrorMessage  string    `json:"error_message,omitempty"`
}

type Finding struct {
	ScanID         string `json:"scan_id"`
	VulnID         string `json:"vuln_id"`
	Severity       string `json:"severity"`
	PackageName    string `json:"package_name"`
	PackageVersion string `json:"package_version"`
	PackageType    string `json:"package_type"`
	FixedVersion   string `json:"fixed_version,omitempty"`
}

type ScannerDBState struct {
	Scanner   string    `json:"scanner"`
	Version   string    `json:"version"`
	Checksum  string    `json:"checksum"`
	UpdatedAt time.Time `json:"updated_at"`
}
