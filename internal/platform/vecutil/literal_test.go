package vecutil

import "testing"

func TestLiteralIsAPgvectorTextLiteral(t *testing.T) {
	if got := Literal([]float32{0.5, -1}); got != "[0.500000,-1.000000]" {
		t.Fatalf("unexpected vector literal %q", got)
	}
}
