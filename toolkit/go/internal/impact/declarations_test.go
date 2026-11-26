package impact

import (
	"reflect"
	"testing"
)

func TestGoDeclarationChangeKeepsPackageImpact(t *testing.T) {
	for _, tc := range []struct{ source, old, new, symbol string }{
		{"package core\ntype Value int\n", "int", "string", "Value"},
		{"package core\ntype Value = int\n", "int", "string", "Value"},
		{"package core\nconst Value = 1\n", "1", "2", "Value"},
		{"package core\nvar Value = 1\n", "1", "2", "Value"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			r := runChange(t, map[string]string{
				"go.mod":               "module example.com/sample\n\ngo 1.25.0\n",
				"core/value.go":        tc.source,
				"tests/core_test.go":   "package tests\nimport _ \"example.com/sample/core\"\n",
				"tests/helper_test.go": "package tests\nfunc helper() {}\n",
			}, "core/value.go", tc.old, tc.new)
			if r.Scope != "focused" || !reflect.DeepEqual(r.TestFiles, []string{"tests/core_test.go", "tests/helper_test.go"}) {
				t.Fatalf("result: %+v", r)
			}
			change := r.Changes[0]
			if len(change.Before) != 1 || len(change.After) != 1 || change.Before[0].Name != tc.symbol || change.After[0].Name != tc.symbol {
				t.Fatalf("declarations missing: %+v", change)
			}
		})
	}
}

func TestPythonAssignmentChangeUsesSymbolSeed(t *testing.T) {
	r := runChange(t, map[string]string{
		"src/values.py":       "VALUE = 1\nOTHER = 2\n",
		"tests/test_value.py": "from ..src.values import VALUE\n",
		"tests/test_other.py": "from ..src.values import OTHER\n",
	}, "src/values.py", "VALUE = 1", "VALUE = 3")
	if r.Scope != "focused" || !reflect.DeepEqual(r.TestFiles, []string{"tests/test_value.py"}) {
		t.Fatalf("assignment lost symbol impact: %+v", r)
	}
	change := r.Changes[0]
	if len(change.Before) != 1 || len(change.After) != 1 || change.Before[0].Name != "VALUE" || change.After[0].Name != "VALUE" {
		t.Fatalf("assignment declaration missing: %+v", change)
	}
}

func TestUncoveredExpressionUsesFileSeed(t *testing.T) {
	r := runChange(t, map[string]string{
		"src/values.py":       "VALUE = 1\nOTHER = 2\nVALUE + OTHER\n",
		"tests/test_value.py": "from ..src.values import VALUE\n",
		"tests/test_other.py": "from ..src.values import OTHER\n",
	}, "src/values.py", "VALUE + OTHER", "VALUE - OTHER")
	if !reflect.DeepEqual(r.TestFiles, []string{"tests/test_other.py", "tests/test_value.py"}) {
		t.Fatalf("uncovered expression lost file impact: %+v", r)
	}
	if len(r.Changes[0].After) != 0 {
		t.Fatalf("invented expression declaration: %+v", r.Changes[0])
	}
}

// +case=A test calling the changed function is recommended even when its shortest impact route is an import.
func TestDirectTestSelection(t *testing.T) {
	result := runChange(t, map[string]string{
		"src/value.ts":        "export function value() {return 1;}\nexport function other(){return 0;}\n",
		"tests/value.test.ts": "import {value} from '../src/value';\nexport function testValue(){return value();}\n",
		"tests/other.test.ts": "import {value, other} from '../src/value';\nexport function testOther(){return other();}\n",
	}, "src/value.ts", "return 1", "return 2")
	if !reflect.DeepEqual(result.TestSelection.Files, []string{"tests/value.test.ts"}) || len(result.TestSelection.UnmatchedSources) != 0 {
		t.Fatalf("selection: %+v", result.TestSelection)
	}
}
