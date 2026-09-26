package channels

import "testing"

func TestWeekendOverridesAndOvernightBlocks(t *testing.T) {
	for _, tc := range []struct {
		days    string
		weekday int
		want    bool
	}{
		{"sat", 6, true}, {"sat", 0, false}, {"sun", 0, true}, {"sun", 6, false},
		{"weekend", 0, true}, {"weekend", 6, true}, {"weekend", 3, false},
		{"", 2, true}, {"invalid", 6, false},
	} {
		b := Block{Days: tc.days}
		if got := b.appliesOn(tc.weekday); got != tc.want {
			t.Errorf("%q day %d = %v, want %v", tc.days, tc.weekday, got, tc.want)
		}
	}
	block := Block{Days: "sat", Start: 23 * 60, End: 25 * 60}
	if !block.covers(timeOfWeek{weekday: 0, minutes: 30}) {
		t.Fatal("Saturday night did not carry into Sunday")
	}
	if block.covers(timeOfWeek{weekday: 1, minutes: 30}) {
		t.Fatal("Saturday block continued into Monday")
	}
}
