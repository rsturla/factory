package parser

import (
	"bufio"
	"bytes"
	"regexp"
	"strings"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
)

// DebianCVEContent is the vendor note content structure for Debian.
type DebianCVEContent struct {
	Description string                       `json:"description,omitempty"`
	Notes       []string                     `json:"notes,omitempty"`
	Advisories  []string                     `json:"advisories,omitempty"`
	Packages    map[string][]DebianPkgStatus `json:"packages,omitempty"`
}

// DebianPkgStatus is the per-release status of a package for a CVE.
type DebianPkgStatus struct {
	Release string `json:"release,omitempty"`
	Version string `json:"version,omitempty"`
	Status  string `json:"status,omitempty"`
	Note    string `json:"note,omitempty"`
}

var (
	cveLineRe     = regexp.MustCompile(`^(CVE-\d{4}-\d+)\s*(.*)`)
	noteLineRe    = regexp.MustCompile(`^\s+NOTE:\s*(.+)`)
	advisoryRe    = regexp.MustCompile(`^\s+\{((?:DSA|DLA)-\d+-\d+)\}`)
	unstablePkgRe = regexp.MustCompile(`^\s+-\s+(\S+)\s+(.*)`)
	releasePkgRe  = regexp.MustCompile(`^\s+\[(\S+)\]\s+-\s+(\S+)\s+(.*)`)
	statusTagRe   = regexp.MustCompile(`<([^>]+)>`)
)

// ParseDebianCVEList parses the plaintext data/CVE/list from the Debian
// Security Tracker. Returns a map of CVE ID → vendor note content.
func ParseDebianCVEList(data []byte) (map[string]*DebianCVEContent, error) {
	result := make(map[string]*DebianCVEContent)
	var current *DebianCVEContent

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)

	for scanner.Scan() {
		line := scanner.Text()

		if m := cveLineRe.FindStringSubmatch(line); m != nil {
			desc := strings.TrimSpace(m[2])
			desc = strings.TrimPrefix(desc, "(")
			desc = strings.TrimSuffix(desc, ")")
			current = &DebianCVEContent{
				Description: desc,
				Packages:    make(map[string][]DebianPkgStatus),
			}
			result[m[1]] = current
			continue
		}

		if current == nil {
			continue
		}

		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "  ") {
			current = nil
			continue
		}

		if m := noteLineRe.FindStringSubmatch(line); m != nil {
			current.Notes = append(current.Notes, strings.TrimSpace(m[1]))
			continue
		}

		if m := advisoryRe.FindStringSubmatch(line); m != nil {
			current.Advisories = append(current.Advisories, m[1])
			continue
		}

		if m := releasePkgRe.FindStringSubmatch(line); m != nil {
			pkg := m[2]
			status := parseStatus(m[1], strings.TrimSpace(m[3]))
			current.Packages[pkg] = append(current.Packages[pkg], status)
			continue
		}

		if m := unstablePkgRe.FindStringSubmatch(line); m != nil {
			pkg := m[1]
			status := parseStatus("unstable", strings.TrimSpace(m[2]))
			current.Packages[pkg] = append(current.Packages[pkg], status)
			continue
		}
	}

	return result, scanner.Err()
}

func parseStatus(release, rest string) DebianPkgStatus {
	status := DebianPkgStatus{Release: release}

	if sm := statusTagRe.FindStringSubmatch(rest); sm != nil {
		status.Status = sm[1]
		remainder := strings.TrimSpace(statusTagRe.ReplaceAllString(rest, ""))
		remainder = strings.TrimPrefix(remainder, "(")
		remainder = strings.TrimSuffix(remainder, ")")
		if remainder != "" {
			status.Note = remainder
		}
	} else {
		status.Version = rest
		status.Status = "fixed"
	}

	return status
}

// ToVendorNotes converts parsed Debian CVE entries to model.VendorNote slice.
func ToVendorNotes(entries map[string]*DebianCVEContent) []model.VendorNote {
	notes := make([]model.VendorNote, 0, len(entries))
	for cveID, content := range entries {
		notes = append(notes, model.VendorNote{
			CVEID:   cveID,
			Vendor:  "debian",
			Content: content,
		})
	}
	return notes
}
