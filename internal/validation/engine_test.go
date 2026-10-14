package validation

import (
	"testing"
)

const testSpec = "../../testdata/specs/users.yaml"

func loadTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := Load(testSpec)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestRoutingMatchesTheValidator guards the router we build against the one the validator
// builds internally (which the library does not expose). For every request, Humbug must
// report an operation exactly when the validator found one: if the two disagree, a request
// that was validated would be reported with no operation, or the reverse.
func TestRoutingMatchesTheValidator(t *testing.T) {
	e := loadTestEngine(t)
	cases := []struct{ method, url string }{
		{"POST", "http://example.test/users"},
		{"POST", "http://example.test/users/"},
		{"POST", "http://example.test//users"},
		{"POST", "http://example.test/users//"},
		{"POST", "http://example.test/users/?x=1"},
		{"POST", "http://example.test/users?x=1"},
		{"POST", "http://example.test/users#frag"},
		{"POST", "https://other.test/users"},      // host is not checked
		{"POST", "https://other.test:8443/users"}, // port is not checked
		{"GET", "http://example.test/users"},      // path exists, method does not
		{"DELETE", "http://example.test/users/7"}, // path exists, method does not
		{"GET", "http://example.test/users/7"},
		{"GET", "http://example.test/users/7/"},
		{"GET", "http://example.test/users/%37"},
		{"GET", "http://example.test/users/abc"},
		{"GET", "http://example.test/Users"}, // wrong case: not routed
		{"GET", "http://example.test/nope"},
		{"GET", "http://example.test/"},
		{"GET", "http://example.test"},
		{"GET", "http://example.test/label/.5"},
		{"GET", "http://example.test/matrix/;id=5"},
		{"GET", "http://example.test/list/1,2,3"},
		{"GET", "http://example.test/items"},
		{"GET", "http://example.test/hdr"},
		{"PUT", "http://example.test/vcl"},
		{"POST", "http://example.test/addresses"},
		{"POST", "http://example.test/contacts-any"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.url, func(t *testing.T) {
			req, err := Request{Method: c.method, URL: c.url}.http()
			if err != nil {
				t.Fatal(err)
			}
			_, errs := e.v.ValidateHttpRequest(req)
			libraryRouted := true
			for _, ve := range errs {
				if ve.ValidationType == "path" && (ve.ValidationSubType == "missing" || ve.ValidationSubType == "missingOperation") {
					libraryRouted = false
				}
			}
			route, _ := e.rt.FindRoute(req)
			ours := route != nil && route.Operation != nil
			if ours != libraryRouted {
				t.Errorf("Humbug routed=%v, validator routed=%v", ours, libraryRouted)
			}

			// The same answer must reach the result.
			res, err := e.Validate(Params{Request: Request{Method: c.method, URL: c.url}, ValidateRequest: true})
			if err != nil {
				t.Fatal(err)
			}
			if (res.Operation != nil) != libraryRouted {
				t.Errorf("result operation=%v, validator routed=%v", res.Operation, libraryRouted)
			}
		})
	}
}
