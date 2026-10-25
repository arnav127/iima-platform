package attrs

import (
	"testing"
	"time"

	"github.com/arnav127/iima-platform/core/store"
)

func TestDerive(t *testing.T) {
	oct2026 := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	may2027 := time.Date(2027, 5, 31, 12, 0, 0, 0, time.UTC)
	jun2027 := time.Date(2027, 6, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		email  string
		member bool
		o      *store.Override
		now    time.Time
		want   Attributes
	}{
		{"PGP1", "p26diya@iima.ac.in", true, nil, oct2026, Attributes{Student, "PGP", 2026, 1}},
		{"FABM1, mixed case", "F26Arjun@IIMA.ac.in", true, nil, oct2026, Attributes{Student, "PGP-FABM", 2026, 1}},
		{"PGP2", "p25aarav@iima.ac.in", true, nil, oct2026, Attributes{Student, "PGP", 2025, 2}},
		{"PGP2 in May is still year 2", "p25aarav@iima.ac.in", true, nil, may2027, Attributes{Student, "PGP", 2025, 2}},
		{"PGP batch done by June", "p25aarav@iima.ac.in", true, nil, jun2027, Attributes{Alum, "PGP", 2025, 0}},
		{"PGPX", "x26rahul@iima.ac.in", true, nil, oct2026, Attributes{Student, "PGPX", 2026, 1}},
		{"PGPX still a student in May", "x26rahul@iima.ac.in", true, nil, may2027, Attributes{Student, "PGPX", 2026, 1}},
		{"PGPX is one year", "x26rahul@iima.ac.in", true, nil, jun2027, Attributes{Alum, "PGPX", 2026, 0}},
		{"AFP", "afp26singh@iima.ac.in", true, nil, oct2026, Attributes{Student, "AFP", 2026, 1}},
		{"AFP is one year", "afp25singh@iima.ac.in", true, nil, oct2026, Attributes{Alum, "AFP", 2025, 0}},
		{"PhD has no fixed length", "phd21neha@iima.ac.in", true, nil, oct2026, Attributes{Student, "PhD", 2021, 6}},
		{"unknown programme", "bpgpbaai26rommeln@iima.ac.in", true, nil, oct2026, Attributes{Student, "BPGPBAAI", 2026, 1}},
		{"faculty or staff", "meena.k@iima.ac.in", true, nil, oct2026, Attributes{Faculty, "", 0, 0}},
		{"admitted, not started", "p27zoya@iima.ac.in", true, nil, oct2026, Attributes{Student, "PGP", 2027, 0}},
		{"gmail", "p25aarav@gmail.com", false, nil, oct2026, Attributes{External, "", 0, 0}},
		{"override staff", "meena.k@iima.ac.in", true, &store.Override{Affiliation: Staff}, oct2026, Attributes{Staff, "", 0, 0}},
		{"override batch (repeated a year)", "p24kabir@iima.ac.in", true, &store.Override{Batch: 2025}, oct2026, Attributes{Student, "PGP", 2025, 2}},
		{"override keeps a student past the usual length", "p24kabir@iima.ac.in", true, &store.Override{Affiliation: Student}, oct2026, Attributes{Student, "PGP", 2024, 3}},
		{"override an exchange student", "visitor@iima.ac.in", true, &store.Override{Affiliation: Student, Programme: "Exchange", Batch: 2026}, oct2026, Attributes{Student, "Exchange", 2026, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Derive(c.email, c.member, c.o, c.now); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
