// Package attrs works out facts about a person from their email: affiliation, programme, batch and
// year. They are facts, not grants: apps decide what to do with them. Super admins correct
// mistakes with overrides; a roll list from the institute could replace the guessing later.
package attrs

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arnav127/iima-platform/core/store"
)

// Attributes go into the token as affiliation, programme, batch and year.
type Attributes struct {
	Affiliation string // student, faculty, alum, external (staff only by override)
	Programme   string // PGP, PGP-FABM, PhD, or the address prefix in capitals
	Batch       int    // year of entry, e.g. 2025
	Year        int    // 1 for first years; 0 when unknown or not a student
}

const (
	Student  = "student"
	Faculty  = "faculty"
	Staff    = "staff"
	Alum     = "alum"
	External = "external"
)

// Affiliations are the values an override may set.
var Affiliations = []string{Student, Faculty, Staff, Alum, External}

type programme struct {
	name  string
	years int // 0: unknown length, never turned into alum automatically
}

// Known address prefixes. Anything else that looks like a roll number still counts as a student,
// with the prefix as its programme name.
var programmes = map[string]programme{
	"p":   {"PGP", 2},
	"f":   {"PGP-FABM", 2},
	"x":   {"PGPX", 1}, // x26: the 2026-27 batch
	"afp": {"AFP", 1},  // Armed Forces Programme; afp26: 2026-27
	"phd": {"PhD", 0},
}

// rollRe matches student addresses: up to 10 letters, then the two-digit entry year
// (p25aarav, f26arjun, phd23neha, bpgpbaai26rommeln). Same rule as Garba's cohorts.
var rollRe = regexp.MustCompile(`^([a-z]{1,10})(\d{2})`)

// AcademicYearStartMonth: a new batch is "year 1" from June.
const AcademicYearStartMonth = time.June

// Derive returns the attributes for a signed-in person. member is the platform's membership
// (a verified @iima.ac.in Google account); everyone else is external.
func Derive(email string, member bool, o *store.Override, now time.Time) Attributes {
	var a Attributes
	if !member {
		a.Affiliation = External
	} else {
		local, _, _ := strings.Cut(store.NormEmail(email), "@")
		if m := rollRe.FindStringSubmatch(local); m != nil {
			yy, _ := strconv.Atoi(m[2])
			a.Affiliation, a.Batch = Student, 2000+yy
			if p, ok := programmes[m[1]]; ok {
				a.Programme = p.name
			} else {
				a.Programme = strings.ToUpper(m[1])
			}
		} else {
			a.Affiliation = Faculty // faculty and staff alike; overrides tell them apart
		}
	}
	if o != nil {
		if o.Affiliation != "" {
			a.Affiliation = o.Affiliation
		}
		if o.Programme != "" {
			a.Programme = o.Programme
		}
		if o.Batch != 0 {
			a.Batch = o.Batch
		}
	}
	if a.Affiliation == Student && a.Batch > 0 {
		a.Year = academicYear(now) - a.Batch + 1
		length := 0
		for _, p := range programmes {
			if p.name == a.Programme {
				length = p.years
			}
		}
		switch {
		case a.Year < 1:
			a.Year = 0 // admitted but not started
		case length > 0 && a.Year > length && (o == nil || o.Affiliation == ""):
			a.Affiliation, a.Year = Alum, 0
		}
	}
	if a.Affiliation != Student {
		a.Year = 0
	}
	return a
}

// academicYear is the calendar year the current academic year started in (IST).
func academicYear(now time.Time) int {
	ist := now.In(time.FixedZone("IST", 5*3600+1800))
	if ist.Month() < AcademicYearStartMonth {
		return ist.Year() - 1
	}
	return ist.Year()
}
