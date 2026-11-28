package homelab

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/aptmark"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// The key order rule is "match the TypeScript expression". rclone.ts sorts with
// Object.keys(settings).sort() and aptmark.ts with [...names].sort(): JavaScript's default sort,
// by UTF-16 code units, with no integer-first rule. That differs from Go's byte order wherever a
// character outside the Basic Multilingual Plane (a surrogate pair, D800-DFFF) meets one in
// E000-FFFF: UTF-16 puts the astral one first, UTF-8 bytes put it last. This compares both
// orders with Node. It needs node on PATH (or in $JSCOMPAT_NODE) and skips without it.
func TestJavaScriptSortOrderAgainstNode(t *testing.T) {
	node := os.Getenv("JSCOMPAT_NODE")
	if node == "" {
		var err error
		if node, err = exec.LookPath("node"); err != nil {
			if os.Getenv("JSCOMPAT_REQUIRE_NODE") != "" {
				t.Fatal("node not found, and JSCOMPAT_REQUIRE_NODE says this run must compare against it")
			}
			t.Skip("node not found; set JSCOMPAT_NODE to run the differential test")
		}
	}
	keys := []string{"", "\U0001F600", "￿", "10", "2", "B", "a", "é", "\U00010000", "ﬁ"}
	in, _ := json.Marshal(keys)
	cmd := exec.Command(node, "-e", `const k = JSON.parse(require("fs").readFileSync(0, "utf8"));
const o = {}; for (const x of k) o[x] = "v";
process.stdout.write(JSON.stringify({ keys: Object.keys(o).sort(), names: [...k].sort() }));`)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var want struct{ Keys, Names []string }
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	got := slices.SortedFunc(slices.Values(keys), jscompat.CompareUTF16)
	if !reflect.DeepEqual(got, want.Keys) {
		t.Errorf("UTF-16 sort order %q, Node's Object.keys(x).sort() %q", got, want.Keys)
	}
	command, _ := aptmark.MarkManualCommand(keys)
	quoted := []string{}
	for _, n := range want.Names {
		quoted = append(quoted, shellQuote(n))
	}
	if wantCommand := "apt-mark manual " + strings.Join(quoted, " ") + " >/dev/null"; command != wantCommand {
		t.Errorf("MarkManualCommand %q, Node's [...names].sort() gives %q", command, wantCommand)
	}
	// the pair that tells the two orders apart is really in the input
	if bytesOrder := slices.Sorted(slices.Values(keys)); reflect.DeepEqual(bytesOrder, want.Keys) {
		t.Fatal("the cases do not separate byte order from UTF-16 order")
	}
}
