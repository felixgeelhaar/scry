package schema

import "testing"

func TestValidateQueryAccepts(t *testing.T) {
	sdl := BuildSDL(fixtureSchema())
	errs := ValidateQuery(sdl, `{ customer(id: "1") { id email } }`)
	if len(errs) != 0 {
		t.Fatalf("expected valid, got %+v", errs)
	}
}

func TestValidateQueryRejectsUnknownField(t *testing.T) {
	sdl := BuildSDL(fixtureSchema())
	errs := ValidateQuery(sdl, `{ customer(id: "1") { phone } }`)
	if len(errs) == 0 {
		t.Fatalf("expected validation errors for unknown field 'phone'")
	}
}

func TestValidateQueryRejectsMissingArg(t *testing.T) {
	sdl := BuildSDL(fixtureSchema())
	errs := ValidateQuery(sdl, `{ customer { id } }`)
	if len(errs) == 0 {
		t.Fatalf("expected validation errors for missing required arg 'id'")
	}
}

func TestEstimateCostCountsFieldsAndDepth(t *testing.T) {
	sdl := BuildSDL(fixtureSchema())
	rpt, errs := EstimateCost(sdl, `{ customer(id: "1") { id email } }`)
	if len(errs) > 0 {
		t.Fatalf("validation errors: %+v", errs)
	}
	if rpt.Fields == 0 {
		t.Errorf("expected nonzero fields")
	}
	if rpt.Depth < 2 {
		t.Errorf("expected depth >= 2 (Query + Customer), got %d", rpt.Depth)
	}
	if rpt.Complexity == 0 {
		t.Errorf("expected nonzero complexity")
	}
}

func TestEstimateCostListMultiplier(t *testing.T) {
	sdl := BuildSDL(fixtureSchema())
	scalar, _ := EstimateCost(sdl, `{ customer(id: "1") { id } }`)
	list, _ := EstimateCost(sdl, `{ orders { id } }`)
	if list.Complexity <= scalar.Complexity {
		t.Errorf("expected list query to cost more than scalar (got scalar=%d, list=%d)", scalar.Complexity, list.Complexity)
	}
	if list.Lists == 0 {
		t.Errorf("expected lists > 0 for orders query")
	}
}

func TestEstimateCostInvalidQueryReturnsErrors(t *testing.T) {
	sdl := BuildSDL(fixtureSchema())
	_, errs := EstimateCost(sdl, `{ noSuchField }`)
	if len(errs) == 0 {
		t.Fatalf("expected validation errors")
	}
}
