package contracts

import "testing"

func TestNodeActivationEvidence_Valid(t *testing.T) {
	if !ExitDataPlaneVerified.Valid() {
		t.Error("ExitDataPlaneVerified must be valid")
	}
	if NodeActivationEvidence("").Valid() {
		t.Error("empty evidence must be invalid")
	}
	if NodeActivationEvidence("something-else").Valid() {
		t.Error("unknown evidence must be invalid")
	}
}
