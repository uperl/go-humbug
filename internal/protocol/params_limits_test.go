package protocol

import (
	"sort"
	"testing"
)

// KNOWN LIBRARY LIMITATIONS: parameter handling in the pinned pb33f version.
//
// Every case records two outcomes: what OpenAPI says (want: "V" valid, "I" invalid, "?" ambiguous
// or unspecified) and what the pinned library does (got). The test asserts only `got`, so it fails
// when a library upgrade changes behavior, whether it got better or worse. The groups:
//
//	works                 the library is right (a regression guard for behavior we rely on)
//	array-items           array items skip most keywords, in every location
//	query-array-explode   default query arrays (form, explode) are validated per occurrence
//	array-level           header, cookie and path arrays skip minItems/maxItems/uniqueItems
//	label-matrix          label and matrix arrays are not decoded
//	lenient               values the library accepts (or rejects) that OpenAPI would not
//	observed              ambiguous in OpenAPI; pinned so a change is noticed
//
// If a case fails: decide whether the new behavior is an improvement. If so, update `got` here and
// the matching bullet in docs/protocol.md ("Known library limitations"); if it is a regression,
// investigate before upgrading the pin.
type paramCase struct {
	group  string
	url    string // path and query as sent
	header string // value of X-V, when set
	cookie string // value of the Cookie header, when set
	want   string
	got    string
	note   string
}

var paramCases = []paramCase{
	// ---- works: query scalars
	{"works", "/q/limit?limit=5", "", "", "V", "V", ""},
	{"works", "/q/limit?limit=0", "", "", "I", "I", "below minimum"},
	{"works", "/q/limit?limit=101", "", "", "I", "I", "above maximum"},
	{"works", "/q/limit?limit=abc", "", "", "I", "I", ""},
	{"works", "/q/limit?limit=1.5", "", "", "I", "I", "not an integer"},
	{"works", "/q/limit?limit=", "", "", "I", "I", "an EMPTY query value is validated, not treated as absent"},
	{"works", "/q/limit?limit", "", "", "I", "I", "no '=' is an empty value"},
	{"works", "/q/limit?lim%69t=5", "", "", "V", "V", "percent-encoded name"},
	{"works", "/q/limit?limit=%35", "", "", "V", "V", "percent-encoded value"},
	{"works", "/q/limit?other=1", "", "", "V", "V", "undeclared parameters are ignored"},
	{"works", "/q/limit?LIMIT=0", "", "", "V", "V", "names are case-sensitive, so this is undeclared"},
	{"works", "/q/limit?limit=5&limit=abc", "", "", "I", "I", "every repeated value is checked"},
	{"works", "/q/limit?limit=0&limit=5", "", "", "I", "I", "every repeated value is checked"},
	{"works", "/q/req", "", "", "I", "I", "required and absent"},
	{"works", "/q/req?req=", "", "", "I", "I", "required and empty (minLength 2)"},
	{"works", "/q/req?req=a", "", "", "I", "I", ""},
	{"works", "/q/req?req=ab", "", "", "V", "V", ""},
	{"works", "/q/req?req=%E2%82%AC%E2%82%AC", "", "", "V", "V", "length counts characters"},
	{"works", "/q/req?req=%E2%82%AC", "", "", "I", "I", ""},
	{"works", "/q/note?note=", "", "", "V", "V", "an empty string the schema allows"},
	{"works", "/q/note?note", "", "", "V", "V", ""},
	{"works", "/q/flag?flag=", "", "", "V", "V", "allowEmptyValue: true"},
	{"works", "/q/flag?flag", "", "", "V", "V", "allowEmptyValue: true"},
	{"works", "/q/active?active=true", "", "", "V", "V", ""},
	{"works", "/q/active?active=false", "", "", "V", "V", ""},
	{"works", "/q/active?active=yes", "", "", "I", "I", ""},
	{"works", "/q/active?active=", "", "", "I", "I", ""},
	{"works", "/q/ratio?ratio=1.5", "", "", "V", "V", ""},
	{"works", "/q/ratio?ratio=NaN", "", "", "I", "I", ""},
	{"works", "/q/ratio?ratio=0x10", "", "", "I", "I", ""},
	{"works", "/q/ratio?ratio=abc", "", "", "I", "I", ""},
	{"works", "/q/pat?pat=a%20b", "", "", "V", "V", ""},
	{"works", "/q/pat?pat=a+b", "", "", "V", "V", "'+' in a query decodes to a space"},
	{"works", "/q/pat2?pat2=a%2Bb", "", "", "V", "V", "so a literal plus must be %2B"},
	{"works", "/q/pat2?pat2=a+b", "", "", "I", "I", "'+' is a space, not a plus"},
	// ---- works: query arrays and objects
	{"works", "/q/ids?ids=1,2,3", "", "", "V", "V", "form, no explode"},
	{"works", "/q/ids?ids=1,x,3", "", "", "I", "I", ""},
	{"works", "/q/ids?ids=1,,3", "", "", "I", "I", ""},
	{"works", "/q/arr?arr=1&arr=2", "", "", "V", "V", ""},
	{"works", "/q/arr?arr=1&arr=x", "", "", "I", "I", "item type is enforced"},
	{"works", "/q/arr?arr=1", "", "", "V", "V", ""},
	{"works", "/q/filter?filter[a]=1&filter[b]=x", "", "", "V", "V", "deepObject"},
	{"works", "/q/filter?filter%5Ba%5D=1", "", "", "V", "V", "encoded brackets"},
	{"works", "/q/filter?filter[a]=x", "", "", "I", "I", ""},
	{"works", "/q/filter?filter[b]=x", "", "", "I", "I", "required property missing"},
	{"works", "/q/filter?filter[a]=1&filter[c]=9", "", "", "V", "V", "extra property allowed"},
	{"works", "/q/obj?obj=a,1,b,2", "", "", "V", "V", "form object, no explode"},
	{"works", "/q/obj?obj=a,x", "", "", "I", "I", ""},
	{"works", "/a/qn?v=1,2", "", "", "V", "V", "array-level constraints work without explode"},
	{"works", "/a/qn?v=1", "", "", "I", "I", "minItems"},
	{"works", "/a/qn?v=1,2,3,4", "", "", "I", "I", "maxItems"},
	{"works", "/a/qn?v=1,1", "", "", "I", "I", "uniqueItems"},
	{"works", "/a/pipe?v=1|2", "", "", "V", "V", "pipeDelimited"},
	{"works", "/a/pipe?v=1", "", "", "I", "I", "minItems"},
	{"works", "/a/pipe?v=1|2|3|4", "", "", "I", "I", "maxItems"},
	{"works", "/a/pipe?v=1|1", "", "", "I", "I", "uniqueItems"},
	{"works", "/i/q?v=5&v=x", "", "", "I", "I", "item type is enforced"},
	{"works", "/i/qn?v=5,10", "", "", "V", "V", ""},
	{"works", "/a/qe?v=c", "", "", "V", "V", "item enum"},
	{"works", "/a/qe?v=a", "", "", "I", "I", "item enum"},
	// ---- works: cookies
	{"works", "/c/session", "", "session=abcd", "V", "V", ""},
	{"works", "/c/session", "", "session=abc", "I", "I", "minLength"},
	{"works", "/c/session", "", "", "I", "I", "required and absent"},
	{"works", "/c/session", "", "session=", "I", "I", "an EMPTY cookie value is validated, not treated as absent"},
	{"works", "/c/session", "", "Session=abcd", "I", "I", "cookie names are case-sensitive"},
	{"works", "/c/session", "", "a=1; session=abcd; b=2", "V", "V", ""},
	{"works", "/c/session", "", `session="abcd"`, "V", "V", "a quoted value (RFC 6265)"},
	{"works", "/c/n", "", "n=5", "V", "V", ""},
	{"works", "/c/n", "", "n=x", "I", "I", ""},
	{"works", "/c/n", "", "n=", "I", "I", ""},
	// ---- works: path
	{"works", "/p/int/5", "", "", "V", "V", ""},
	{"works", "/p/int/-5", "", "", "V", "V", ""},
	{"works", "/p/int/abc", "", "", "I", "I", ""},
	{"works", "/p/int/%35", "", "", "V", "V", ""},
	{"works", "/p/plus/a+b", "", "", "V", "V", "'+' is literal in a path"},
	{"works", "/p/plus/a%2Bb", "", "", "V", "V", ""},
	{"works", "/p/slash/a%2Fb", "", "", "V", "V", "an encoded slash stays inside one segment"},
	{"works", "/p/arr/1,2,3", "", "", "V", "V", "simple array"},
	{"works", "/p/arr/1,x,3", "", "", "I", "I", ""},
	{"works", "/p/obj/a,1,b,2", "", "", "V", "V", "simple object"},
	{"works", "/p/obj/a,x", "", "", "I", "I", ""},
	{"works", "/a/h", "1,2", "", "V", "V", ""},
	{"works", "/a/c", "", "v=1,2", "V", "V", ""},
	{"works", "/a/p/1,2", "", "", "V", "V", ""},

	// ---- array-items: item keywords are not enforced, in any location (item type and enum are)
	{"array-items", "/i/q?v=5&v=3", "", "", "I", "V", "minimum 5, form with explode"},
	{"array-items", "/i/q?v=5&v=15", "", "", "I", "V", "maximum 10"},
	{"array-items", "/i/q?v=5&v=7", "", "", "I", "V", "multipleOf 5"},
	{"array-items", "/i/qn?v=5,3", "", "", "I", "V", "minimum, form without explode"},
	{"array-items", "/i/qn?v=5,15", "", "", "I", "V", "maximum"},
	{"array-items", "/i/qn?v=5,7", "", "", "I", "V", "multipleOf"},
	{"array-items", "/i/pipe?v=5|3", "", "", "I", "V", "minimum, pipeDelimited"},
	{"array-items", "/i/pipe?v=5|15", "", "", "I", "V", "maximum"},
	{"array-items", "/i/h", "5,3", "", "I", "V", "minimum, header"},
	{"array-items", "/i/h", "5,15", "", "I", "V", "maximum"},
	{"array-items", "/i/c", "", "v=5,3", "I", "V", "minimum, cookie"},
	{"array-items", "/i/c", "", "v=5,15", "I", "V", "maximum"},
	{"array-items", "/i/p/5,3", "", "", "I", "V", "minimum, path"},
	{"array-items", "/i/p/5,15", "", "", "I", "V", "maximum"},
	{"array-items", "/s/q?v=ab,Ab", "", "", "I", "V", "pattern"},
	{"array-items", "/s/q?v=ab,abcd", "", "", "I", "V", "maxLength"},
	{"array-items", "/s/q?v=ab,a", "", "", "I", "V", "minLength"},
	{"array-items", "/s/h", "ab,Ab", "", "I", "V", "pattern, header"},
	{"array-items", "/s/h", "ab,abcd", "", "I", "V", "maxLength, header"},
	{"array-items", "/s/h", "ab,a", "", "I", "V", "minLength, header"},

	// ---- query-array-explode: each repeated value is validated as its own one-item array
	{"query-array-explode", "/a/q?v=1&v=2", "", "", "V", "I", "a VALID two-item array fails minItems 2 (a false violation)"},
	{"query-array-explode", "/a/q?v=1&v=2&v=3", "", "", "V", "I", "a VALID three-item array fails minItems 2"},
	{"query-array-explode", "/a/q?v=1", "", "", "I", "I", "minItems 2 (right answer for the wrong reason)"},
	{"query-array-explode", "/a/q?v=1&v=2&v=3&v=4", "", "", "I", "I", "maxItems 3 (right answer for the wrong reason)"},
	{"query-array-explode", "/a/q?v=1&v=1", "", "", "I", "I", "uniqueItems (right answer for the wrong reason)"},
	{"query-array-explode", "/q/tags?tags=a&tags=b&tags=c&tags=d", "", "", "I", "V", "maxItems 3 alone is missed"},
	{"query-array-explode", "/q/arr?arr=1,2", "", "", "I", "V", "with explode a comma is data: one element \"1,2\", not an integer"},
	{"query-array-explode", "/a/qs?v=a,b", "", "", "I", "V", "the comma splits one element into two"},
	{"query-array-explode", "/a/qs?v=A", "", "", "I", "V", "item pattern is not enforced"},
	{"query-array-explode", "/a/qe?v=a,b", "", "", "V", "I", "enum contains the single value \"a,b\", split into a and b"},

	// ---- array-level: minItems, maxItems and uniqueItems are not enforced
	{"array-level", "/a/h", "1", "", "I", "V", "minItems, header"},
	{"array-level", "/a/h", "1,2,3,4", "", "I", "V", "maxItems, header"},
	{"array-level", "/a/h", "1,1", "", "I", "V", "uniqueItems, header"},
	{"array-level", "/a/c", "", "v=1", "I", "V", "minItems, cookie"},
	{"array-level", "/a/c", "", "v=1,2,3,4", "I", "V", "maxItems, cookie"},
	{"array-level", "/a/c", "", "v=1,1", "I", "V", "uniqueItems, cookie"},
	{"array-level", "/a/p/1", "", "", "I", "V", "minItems, path"},
	{"array-level", "/a/p/1,2,3,4", "", "", "I", "V", "maxItems, path"},
	{"array-level", "/a/p/1,1", "", "", "I", "V", "uniqueItems, path"},

	// ---- label-matrix: arrays are not decoded (primitives: see TestPathParameterStyles)
	{"label-matrix", "/p/lab/.1.2.3", "", "", "V", "I", "a valid label-style array is a false violation"},
	{"label-matrix", "/p/lab/1,2,3", "", "", "I", "V", "not label-encoded, accepted"},
	{"label-matrix", "/p/lab/.1.x.3", "", "", "I", "I", "invalid for the wrong reason"},
	{"label-matrix", "/p/mat/;ids=1,2,3", "", "", "V", "I", "a valid matrix array is a false violation"},
	{"label-matrix", "/p/mat/;ids=1,x,3", "", "", "I", "I", "invalid for the wrong reason"},
	{"label-matrix", "/p/matx/;ids=1;ids=2;ids=3", "", "", "V", "I", "matrix with explode, a false violation"},
	{"label-matrix", "/p/matx/;ids=1;ids=x", "", "", "I", "I", "invalid for the wrong reason"},

	// ---- lenient: accepted although OpenAPI would reject
	{"lenient", "/q/active?active=1", "", "", "I", "V", "booleans are 'true' and 'false' only"},
	{"lenient", "/q/active?active=TRUE", "", "", "I", "V", ""},
	{"lenient", "/q/obj?obj=a,1,b", "", "", "I", "V", "an odd number of items in a key,value list"},

	// ---- observed: ambiguous in OpenAPI, pinned so a change is noticed
	{"observed", "/q/int?int=007", "", "", "?", "V", "leading zeros"},
	{"observed", "/q/int?int=+5", "", "", "?", "I", "but a path integer accepts +5, below"},
	{"observed", "/q/int?int=5.0", "", "", "?", "V", "JSON Schema: 5.0 is an integer"},
	{"observed", "/q/int?int=1e2", "", "", "?", "V", ""},
	{"observed", "/q/int?int=-0", "", "", "?", "V", ""},
	{"observed", "/q/int?int=%205", "", "", "?", "I", "leading space"},
	{"observed", "/q/int?int=9999999999999999999999", "", "", "?", "I", "beyond int64; JSON Schema allows it"},
	{"observed", "/q/ratio?ratio=.5", "", "", "?", "V", "not a JSON number"},
	{"observed", "/q/ratio?ratio=5.", "", "", "?", "V", "not a JSON number"},
	{"observed", "/q/ratio?ratio=+1", "", "", "?", "I", ""},
	{"observed", "/p/int/05", "", "", "?", "V", ""},
	{"observed", "/p/int/+5", "", "", "?", "V", "a query integer rejects +5"},
	{"observed", "/p/int/5.0", "", "", "?", "V", ""},
	{"observed", "/p/int/1e2", "", "", "?", "V", ""},
	{"observed", "/q/ids?ids=1%2C2%2C3", "", "", "?", "V", "encoded commas are decoded before splitting"},
	{"observed", "/q/ids?ids=1&ids=2", "", "", "?", "V", "repeated, though explode is false"},
	{"observed", "/q/ids?ids=", "", "", "?", "I", "an empty list"},
	{"observed", "/q/tags?tags=", "", "", "?", "V", "one empty element"},
	{"observed", "/q/pipe?v=1", "", "", "?", "I", ""},
	{"observed", "/c/session", "", "session=ab; session=abcd", "?", "V", "duplicate cookie name"},
	{"observed", "/c/session", "", "session = abcd", "?", "V", "spaces around ="},
}

func TestParameterLibraryBehavior(t *testing.T) {
	var lines []string
	lines = append(lines, initPath(1, "../../testdata/specs/params.yaml"))
	for i, c := range paramCases {
		var hdrs hdrs
		if c.header != "" {
			hdrs = append(hdrs, [2]string{"X-V", c.header})
		}
		if c.cookie != "" {
			hdrs = append(hdrs, [2]string{"Cookie", c.cookie})
		}
		lines = append(lines, `{"id":`+itoa(i+2)+`,"type":"validate","params":{"request":`+getReq(c.url, hdrs)+`}}`)
	}
	replies := run(t, lines...)

	tally := map[string]int{}
	differing := map[string]int{}
	for i, c := range paramCases {
		tally[c.group]++
		if c.group == "works" && c.want != c.got {
			t.Errorf("table error: a 'works' case must be correct: %s", c.url)
		}
		if c.want != c.got {
			differing[c.group]++
		}
		r := replies[i+1]
		if r["type"] == "error" {
			t.Errorf("%s: %v", c.url, r)
			continue
		}
		res := result(t, r)
		got := "I"
		if res["valid"] == true && res["complete"] == true {
			got = "V"
		}
		if got != c.got {
			t.Errorf("%s [%s]: the library now gives %s; it was pinned as %s (OpenAPI says %s). %s\n"+
				"   If this is an improvement, update the case and the bullet in docs/protocol.md (\"Known library limitations\").",
				c.url+" "+c.header+c.cookie, c.group, got, c.got, c.want, c.note)
		}
	}
	for g := range tally {
		// Every documented limitation must be demonstrated by at least one case where the library
		// differs from OpenAPI (some cases in a group are right for the wrong reason).
		if g != "works" && g != "observed" && differing[g] == 0 {
			t.Errorf("table error: group %q has no case that differs from OpenAPI", g)
		}
	}
	var groups []string
	for g := range tally {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		t.Logf("%-20s %3d cases", g, tally[g])
	}
}
