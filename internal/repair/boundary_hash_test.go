package repair

import "testing"

func TestBoundaryTestHashMatchesDeployedWorkerSuite(t *testing.T) {
	if got := BoundaryTestsSHA256("keep"); got != "f8aa71e8c61e35aadfa80bd2d557c9a9f001df9a4ef825f192391b496830f172" {
		t.Fatalf("unexpected keep suite hash %s", got)
	}
}
