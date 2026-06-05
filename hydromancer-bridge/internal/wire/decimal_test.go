package wire

import "testing"

func TestScaleSigned(t *testing.T) {
	cases := []struct {
		v    int64
		exp  int8
		want string
	}{
		{11337700, -2, "113377"},
		{11337701, -2, "113377.01"},
		{76699, -4, "7.6699"},
		{1, -4, "0.0001"},
		{0, -4, "0"},
		{19900, 0, "19900"},
		{199, 2, "19900"},
		{-12345, -2, "-123.45"},
		{1234500, -2, "12345"},
		{50, -8, "0.0000005"},
	}
	for _, c := range cases {
		if got := ScaleSigned(c.v, c.exp); got != c.want {
			t.Errorf("ScaleSigned(%d, %d) = %q, want %q", c.v, c.exp, got, c.want)
		}
	}
}

func TestScaleUnsigned(t *testing.T) {
	if got := ScaleUnsigned(115430, -6); got != "0.11543" {
		t.Errorf("got %q, want 0.11543", got)
	}
}
